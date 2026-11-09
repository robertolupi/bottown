package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// A retro run's evidence: what bottown recorded of the flow it looks back
// on, gathered by the launcher with the owner key (the run's own key may
// not list runs or read the audit log) into the run's scratch directory:
//
//	retro/README.md                 the runs at a glance, the PRs' commit statuses, and the files
//	retro/runs.json                 each run: the server's record and digest, the local record, its denials
//	retro/transcripts/<id>.jsonl    the run's log, one normalized line per message or tool call
//	retro/prompts/<id>.md           the goal prompt the run was given
//
// Transcripts are in one format for every harness (runs.RenderLog), so the
// retro need not learn each harness's log.

// retroDir is where a retro run's evidence goes, below its scratch directory.
const retroDir = "retro"

// maxTranscriptField bounds each text field of a transcript line: a tool's
// output can be a whole file, and the retro needs what happened, not it.
const maxTranscriptField = 4000

// retroRun is one run of the flow, as the evidence records it.
type retroRun struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Actor   string `json:"actor"`
	Family  string `json:"family"`
	Harness string `json:"harness,omitempty"`
	Spec    string `json:"spec,omitempty"`
	Issue   int64  `json:"issue,omitempty"`
	PR      int64  `json:"pr,omitempty"`
	Branch  string `json:"branch,omitempty"`
	SHA     string `json:"sha,omitempty"`
	// Interactive runs were steered by the owner, in a terminal: they
	// have no log.
	Interactive     bool       `json:"interactive,omitempty"`
	AllowSameFamily bool       `json:"allow_same_family,omitempty"`
	StartedBy       string     `json:"started_by,omitempty"`
	Created         time.Time  `json:"created"`
	Ended           *time.Time `json:"ended,omitempty"`
	// Duration is from creation to end, as the server saw it.
	Duration string `json:"duration,omitempty"`
	// State and Status are the server's: whether the key is live, and
	// how the run ended; Outcome is the work result it saw through its
	// tools.
	State   string `json:"state,omitempty"`
	Status  string `json:"status,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	secrets.Digest
	Purged bool `json:"purged,omitempty"`
	// Build is the bottown binary that launched the run: the server's
	// record of it, or the local one's.
	Build *secrets.Build `json:"build,omitempty"`
	// Local is the launcher's record, when this checkout launched the run.
	Local *retroLocal `json:"local,omitempty"`
	// Denials are the policy refusals the server kept for the run.
	Denials []server.AuditEntry `json:"denials,omitempty"`
	// HarnessDenials are the tool calls the run's harness refused (its
	// transcript's error lines that start with "denied:"), which the
	// policy never saw.
	HarnessDenials int `json:"harness_denials,omitempty"`
	// Transcript and Prompt are the run's files in the evidence, relative
	// to it; empty when there is none.
	Transcript string `json:"transcript,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
}

// retroLocal is what the launcher's record adds; never the run key.
type retroLocal struct {
	Status        string         `json:"status"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	StopRequested bool           `json:"stop_requested,omitempty"`
	Started       time.Time      `json:"started"`
	Ended         *time.Time     `json:"ended,omitempty"`
	Question      string         `json:"question,omitempty"`
	Message       string         `json:"message,omitempty"`
	FlowNote      string         `json:"flow_note,omitempty"`
	ChecksPassed  bool           `json:"checks_passed,omitempty"`
	Handover      *runs.Handover `json:"handover,omitempty"`
}

// gatherRetro writes the evidence of flow, for retro run self, into dir,
// and returns how many runs it found: those flow.Has selects, as the
// server's cross-family rule does. Local files come from ws; the server's
// records from runList (GET /v1/runs?history=1), the denials from audit
// (GET /v1/audit?decision=deny,reject), and forgePRs are the pull requests
// Forgejo has from the flow's branch, tips their heads' own commits, and
// sts the commit statuses at their commits. It also returns the gaps it
// sees in the evidence (retroGaps, and the statuses it could not read),
// which the README and the prompt warn of.
func gatherRetro(dir string, flow server.RetroFlow, self string, ws *runs.Workspace, runList []server.RunItem, audit []server.AuditEntry, forgePRs []int64, tips []prTip, sts prStatuses) (int, []string, error) {
	return gatherRetroScope(dir, retroScope{Flows: []retroFlowPRs{{Flow: flow, ForgePRs: forgePRs}}}, self, ws, runList, audit, tips, sts)
}

// retroScope is what a retro's evidence covers: one flow, or the flows of
// a window's merged pull requests (#383).
type retroScope struct {
	Flows []retroFlowPRs
	// Window is a windowed retro's window, up to Head, and Merged the pull
	// requests its first-parent commits merged, oldest first; nil for a
	// retro of one flow.
	Window *secrets.RetroWindow
	Head   string
	Merged []prHead
}

// retroFlowPRs is a flow and the pull requests Forgejo has from its branch.
type retroFlowPRs struct {
	Flow     server.RetroFlow
	ForgePRs []int64
	// Milestone is, in a window, the milestone pull request whose branch
	// this flow's pull request merged into; 0 for a flow of its own.
	Milestone int64
}

// String names the scope for people: the flow, or the window.
func (sc retroScope) String() string {
	if sc.Window != nil {
		return fmt.Sprintf("the window `%s..%s`", short(sc.Window.Base), short(sc.Head))
	}
	if len(sc.Flows) == 1 {
		return sc.Flows[0].Flow.String()
	}
	return "no flow"
}

// gatherRetroScope is gatherRetro for every flow of scope: a run is in
// the evidence when it belongs to any of them.
func gatherRetroScope(dir string, scope retroScope, self string, ws *runs.Workspace, runList []server.RunItem, audit []server.AuditEntry, tips []prTip, sts prStatuses) (int, []string, error) {
	if err := os.RemoveAll(dir); err != nil {
		return 0, nil, err
	}
	for _, d := range []string{dir, filepath.Join(dir, "transcripts"), filepath.Join(dir, "prompts")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return 0, nil, err
		}
	}
	local := map[string]runs.Record{}
	if recs, err := ws.Records(io.Discard); err == nil {
		for _, r := range recs {
			local[r.ID] = r
		}
	}
	// Every run the server lists, then those it no longer lists but this
	// checkout still records; the flow's pull requests come from all of
	// them before any run is selected.
	var cands []secrets.Run
	var states []string
	listed := map[string]bool{}
	for _, it := range runList {
		if !listed[it.Run.ID] {
			listed[it.Run.ID] = true
			cands, states = append(cands, it.Run), append(states, it.State)
		}
	}
	for _, l := range local {
		if !listed[l.ID] {
			cands = append(cands, secrets.Run{
				ID: l.ID, Kind: l.Kind, Actor: l.Actor, Family: l.Family, Harness: l.Harness, Spec: l.Spec,
				Issue: l.Issue, PR: l.PR, Branch: l.Branch, SHA: l.SHA, Interactive: l.Interactive,
				Created: l.Started, Ended: l.Ended, Build: l.Build,
			})
			states = append(states, "")
		}
	}
	prs := make([]map[int64]bool, len(scope.Flows))
	for i, f := range scope.Flows {
		prs[i] = f.Flow.PRs(f.ForgePRs, cands)
	}
	inScope := func(r secrets.Run) bool {
		for i, f := range scope.Flows {
			if f.Flow.Has(r, prs[i]) {
				return true
			}
		}
		return false
	}

	var out []retroRun
	for i, r := range cands {
		if r.ID == self || !inScope(r) {
			continue
		}
		rr := retroRun{
			ID: r.ID, Kind: r.Kind, Actor: r.Actor, Family: r.Family, Harness: r.Harness, Spec: r.Spec,
			Issue: r.Issue, PR: r.PR, Branch: r.Branch, SHA: r.SHA, Interactive: r.Interactive,
			AllowSameFamily: r.AllowSameFamily, StartedBy: r.StartedBy, Created: r.Created, Ended: r.Ended,
			State: states[i], Status: r.Status, Outcome: r.Outcome, Digest: r.Digest, Purged: r.Purged, Build: r.Build,
		}
		if r.Ended != nil {
			rr.Duration = r.Ended.Sub(r.Created).Round(time.Second).String()
		}
		for _, e := range audit {
			if e.Run == r.ID {
				rr.Denials = append(rr.Denials, e)
			}
		}
		if l, ok := local[r.ID]; ok {
			rr.Local = &retroLocal{
				Status: l.Status, ExitCode: l.ExitCode, StopRequested: l.StopRequested, Started: l.Started, Ended: l.Ended,
				Question: l.Question, Message: l.Message, FlowNote: l.FlowNote, ChecksPassed: l.ChecksPassed, Handover: l.Handover,
			}
			if rr.Harness == "" {
				rr.Harness = l.Harness
			}
			if rr.Build == nil {
				rr.Build = l.Build
			}
		}
		out = append(out, rr)
	}
	slices.SortFunc(out, func(a, b retroRun) int { return a.Created.Compare(b.Created) })

	for i := range out {
		r := &out[i]
		if b, err := runs.ReadLog(ws.Path(r.ID, ".log")); err == nil && len(b) > 0 {
			rel := filepath.Join("transcripts", r.ID+".jsonl")
			denied, err := writeTranscript(filepath.Join(dir, rel), r.Harness, b)
			if err != nil {
				return 0, nil, err
			}
			r.Transcript, r.HarnessDenials = rel, denied
		}
		if b, err := os.ReadFile(ws.Path(r.ID, ".prompt.md")); err == nil {
			rel := filepath.Join("prompts", r.ID+".md")
			if err := os.WriteFile(filepath.Join(dir, rel), b, 0o600); err != nil {
				return 0, nil, err
			}
			r.Prompt = rel
		}
	}
	b, err := json.MarshalIndent(orEmptyRuns(out), "", "  ")
	if err != nil {
		return 0, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), append(b, '\n'), 0o600); err != nil {
		return 0, nil, err
	}
	gaps := append(retroGaps(scope, prs, out, tips), sts.Errors...)
	return len(out), gaps, os.WriteFile(filepath.Join(dir, "README.md"), []byte(retroReadme(scope, out, gaps, sts.Statuses)), 0o600)
}

// prStatus is a commit status at one of a flow pull request's commits.
type prStatus struct {
	PR          int64
	SHA         string
	Context     string
	State       string
	Description string
	Creator     string
	Created     time.Time
}

// prStatuses are the commit statuses at the flow's pull requests' commits,
// and why some could not be read.
type prStatuses struct {
	Statuses []prStatus
	Errors   []string
}

// prTip is a pull request's head and its own last commit: the head or,
// when the head merges another branch in (as the flow's "Merge main into"
// commits do), the first-parent commit below the merges. Author is that
// commit's Forgejo login, or its git author name when it has none.
type prTip struct {
	PR     int64
	Head   string
	SHA    string
	Author string
}

// retroGaps is what the evidence of runs rs, of scope's flows (whose pull
// requests are prs), visibly lacks: missing runs otherwise look like a
// short flow. It looks at each flow apart, since one flow's runs say
// nothing of another's. A flow with verify runs and no implement or plan
// run (in a window, a merged pull request's flow with none) means the runs
// that wrote its code are not in it; a pull request whose own last commit
// (tips) is by an actor with no implement or plan run in its flow was
// pushed by no run in it.
func retroGaps(scope retroScope, prs []map[int64]bool, rs []retroRun, tips []prTip) []string {
	var gaps []string
	writers := make([]map[string]bool, len(scope.Flows))
	verifies := make([]int, len(scope.Flows))
	for i, f := range scope.Flows {
		writers[i] = map[string]bool{}
		for _, r := range rs {
			if !f.Flow.Has(secrets.Run{Issue: r.Issue, PR: r.PR, Branch: r.Branch}, prs[i]) {
				continue
			}
			switch r.Kind {
			case policy.Implement, policy.Plan:
				writers[i][r.Actor] = true
			case policy.Verify:
				verifies[i]++
			}
		}
	}
	// A milestone's pull request carries its children's code: their
	// writers wrote it.
	for j, c := range scope.Flows {
		for i := range scope.Flows {
			if c.Milestone > 0 && i != j && prs[i][c.Milestone] {
				maps.Copy(writers[i], writers[j])
			}
		}
	}
	for i, f := range scope.Flows {
		switch {
		case len(writers[i]) > 0:
		case scope.Window != nil:
			var merged []string
			for _, pr := range scope.Merged {
				for _, p := range append([]prHead{pr}, pr.Children...) {
					if prs[i][p.Number] {
						merged = append(merged, fmt.Sprintf("!%d", p.Number))
					}
				}
			}
			gaps = append(gaps, fmt.Sprintf("Merged pull request %s (flow %s) has %d verify run(s) but no implement or plan run in the evidence: the runs that wrote its code are missing from it.", strings.Join(merged, ", "), f.Flow, verifies[i]))
		case verifies[i] > 0:
			gaps = append(gaps, fmt.Sprintf("The evidence has %d verify run(s) but no implement or plan run: the runs that wrote the code are missing from it.", verifies[i]))
		}
	}
	for _, t := range tips {
		if t.Author == "" {
			continue
		}
		wrote := false
		for i := range scope.Flows {
			wrote = wrote || (prs[i][t.PR] && writers[i][t.Author])
		}
		if wrote {
			continue
		}
		at := "`" + short(t.Head) + "`"
		if t.SHA != t.Head {
			at += fmt.Sprintf(" (its own last commit, below the merges, is `%s`)", short(t.SHA))
		}
		gaps = append(gaps, fmt.Sprintf("Pull request !%d's head %s is by `%s`, who has no implement or plan run in the evidence: no run in it pushed that head. Someone may have pushed it by hand, or its run's record is missing.", t.PR, at, t.Author))
	}
	return gaps
}

func orEmptyRuns(rs []retroRun) []retroRun {
	if rs == nil {
		return []retroRun{}
	}
	return rs
}

// writeTranscript writes a run's log as JSON lines of runs.LogLine, each
// text field bounded by maxTranscriptField, and returns how many tool calls
// the harness denied.
func writeTranscript(path, harness string, log []byte) (int, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(f)
	denied := 0
	for _, l := range runs.RenderLog(harness, log) {
		if l.Kind == runs.LineError && strings.HasPrefix(l.Text, "denied:") {
			denied++
		}
		for _, s := range []*string{&l.Text, &l.Detail, &l.Parameters, &l.Output} {
			if len(*s) > maxTranscriptField {
				kept := secrets.TruncateUTF8(*s, maxTranscriptField)
				*s = fmt.Sprintf("%s [%d bytes cut]", kept, len(*s)-len(strings.TrimSuffix(kept, "…")))
			}
		}
		if err = enc.Encode(l); err != nil {
			break
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return denied, err
}

// retroReadme is the evidence's index: the runs, oldest first, and where
// to read more, then the pull requests' commit statuses (sts). The gaps,
// from retroGaps, come first, as a warning.
func retroReadme(scope retroScope, rs []retroRun, gaps []string, sts []prStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence for the retro of %s\n\n", scope)
	if len(gaps) > 0 {
		b.WriteString("> [!WARNING]\n> **The evidence looks partial.** Say so in the retro, and do not read\n" +
			"> what is missing as a short flow.\n>\n")
		for _, g := range gaps {
			b.WriteString("> - " + g + "\n")
		}
		b.WriteString("\n")
	}
	what := "the issue or its pull requests"
	var flow server.RetroFlow
	if len(scope.Flows) == 1 {
		flow = scope.Flows[0].Flow
	}
	switch {
	case scope.Window != nil:
		what = "the flows of the pull requests merged in the window"
		writeWindow(&b, scope)
	case flow.PR > 0 && flow.Issue > 0:
		fmt.Fprintf(&b, "The retro is on pull request !%d, from `%s`: the evidence is the flow of issue #%d.\n\n", flow.PR, flow.Branch, flow.Issue)
	case flow.PR > 0:
		what = "the pull request or its branch"
	}
	if len(rs) == 0 {
		fmt.Fprintf(&b, "bottown has no record of a run on %s.\n", what)
		writeStatuses(&b, sts)
		return b.String()
	}
	b.WriteString("The runs that worked " + what + ", oldest first. `runs.json`\n" +
		"has each run in full: the server's record and digest (summary, usage, observed failure),\n" +
		"the launcher's record, and the policy refusals (denials). Harness denials are the tool calls\n" +
		"the run's harness refused: its transcript's `error` lines that start with `denied:`. Transcripts are JSON lines,\n" +
		"one per assistant message, tool call or status event, the same format for every harness:\n" +
		"`kind` (text, tool, output, error, status, plain), `tool`, `state`, `text`, `parameters`, `output`.\n\n")
	b.WriteString("| run | kind | actor | target | created | duration | status | outcome | denials | harness denials | transcript | prompt |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		target := fmt.Sprintf("#%d", r.Issue)
		if r.PR > 0 {
			target = fmt.Sprintf("!%d", r.PR)
		}
		if r.SHA != "" {
			target += " @ " + short(r.SHA)
		}
		actor := r.Actor + " (" + r.Family
		if r.Spec != "" {
			actor += ", " + r.Spec
		}
		actor += ")"
		status := orDash(r.Status)
		if r.Failure != nil {
			status += ": " + r.Failure.Cause
		}
		if r.Interactive {
			status += ", interactive"
		}
		transcript := r.Transcript
		if transcript == "" {
			transcript = "none"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %d | %d | %s | %s |\n",
			r.ID, r.Kind, actor, target, r.Created.Format(time.RFC3339), orDash(r.Duration), status,
			orDash(r.Outcome), len(r.Denials), r.HarnessDenials, transcript, orDash(r.Prompt))
	}
	writeStatuses(&b, sts)
	return b.String()
}

// writeWindow writes a windowed retro's window and the pull requests
// merged in it, each with the flow whose runs the evidence holds, a
// milestone's followed by its children.
func writeWindow(b *strings.Builder, scope retroScope) {
	w := scope.Window
	fmt.Fprintf(b, "The window (`--since %s`) is what landed on the default branch after `%s`, up to `%s`:\n"+
		"its first-parent commits, the pull requests they merged, and each one's flow.\n\n", w.Since, w.Base, scope.Head)
	if len(scope.Merged) == 0 {
		b.WriteString("No pull request was merged in the window: its commits were pushed directly.\n\n")
		return
	}
	b.WriteString("| pull request | title | branch | merge commit | flow |\n|---|---|---|---|---|\n")
	for _, pr := range scope.Merged {
		writeWindowPR(b, fmt.Sprintf("!%d", pr.Number), pr)
		// A milestone's children, merged into its branch: `!M → #I (!N)`.
		for _, c := range pr.Children {
			name := fmt.Sprintf("!%d → !%d", pr.Number, c.Number)
			if flow := windowPRFlow(c); flow.Issue > 0 {
				name = fmt.Sprintf("!%d → #%d (!%d)", pr.Number, flow.Issue, c.Number)
			}
			writeWindowPR(b, name, c)
		}
	}
	b.WriteString("\n")
}

// writeWindowPR writes the window table's row of merged pull request pr,
// named name.
func writeWindowPR(b *strings.Builder, name string, pr prHead) {
	fmt.Fprintf(b, "| %s | %s | `%s` | `%s` | %s |\n", name, strings.ReplaceAll(pr.Title, "|", `\|`),
		pr.Head.Ref, short(pr.MergeCommit), windowPRFlow(pr))
}

// windowPRFlow is merged pull request pr's own flow, with the issue
// windowFlows recovered when its branch is gone.
func windowPRFlow(pr prHead) server.RetroFlow {
	flow, _ := retroFlowOf(pr.Number, []prHead{pr})
	if flow.Issue == 0 {
		flow.Issue = pr.Issue // recovered: its branch is gone
	}
	return flow
}

// writeStatuses writes each pull request's commit statuses, oldest first,
// and flags a context with more than one status at a commit (checks that
// overlapped, or re-ran, at one head), and one with a success and a failure.
func writeStatuses(b *strings.Builder, sts []prStatus) {
	if len(sts) == 0 {
		return
	}
	sts = slices.Clone(sts)
	slices.SortStableFunc(sts, func(x, y prStatus) int {
		if c := cmp.Compare(x.PR, y.PR); c != 0 {
			return c
		}
		return x.Created.Compare(y.Created)
	})
	b.WriteString("\n## Commit statuses\n\n" +
		"The statuses at the flow's pull requests' commits and at every head a run saw, oldest first:\n" +
		"`bottown/check` from `bottown check` in a run, `bottown/e2e` from the flow's own checks.\n")
	type key struct{ sha, context string }
	for i := 0; i < len(sts); {
		pr := sts[i].PR
		fmt.Fprintf(b, "\n### !%d\n\n", pr)
		b.WriteString("| commit | context | state | created | description | creator |\n|---|---|---|---|---|---|\n")
		var keys []key
		byKey := map[key][]string{}
		for ; i < len(sts) && sts[i].PR == pr; i++ {
			st := sts[i]
			k := key{st.SHA, st.Context}
			if _, ok := byKey[k]; !ok {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], st.State)
			fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s | %s |\n", short(st.SHA), st.Context, st.State,
				st.Created.Format(time.RFC3339), orDash(strings.ReplaceAll(st.Description, "|", `\|`)), orDash(st.Creator))
		}
		var flags []string
		for _, k := range keys {
			states := byKey[k]
			if len(states) < 2 {
				continue
			}
			f := fmt.Sprintf("`%s` has %d `%s` statuses", short(k.sha), len(states), k.context)
			if slices.Contains(states, "success") && (slices.Contains(states, "failure") || slices.Contains(states, "error")) {
				f += ", a success and a failure"
			}
			flags = append(flags, f+": checks overlapped, or ran again, at that commit.")
		}
		if len(flags) > 0 {
			b.WriteString("\n> [!WARNING]\n")
			for _, f := range flags {
				b.WriteString("> - " + f + "\n")
			}
		}
	}
}

// collectRetro gathers the evidence for retro run run into dir with the
// owner key, and returns the flow it looked back on (from run.Issue alone
// when the pull requests could not be listed), how many runs it found, and
// the gaps it saw in them.
func collectRetro(ctx context.Context, rc *runContext, run secrets.Run, dir string) (server.RetroFlow, int, []string, error) {
	flow := server.NewRetroFlow(run.Issue, false, "")
	heads, err := prHeads(ctx, rc)
	if err != nil {
		return flow, 0, nil, fmt.Errorf("list pull requests: %w", err)
	}
	flow, prs := retroFlowOf(run.Issue, heads)
	var list []server.RunItem
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs?history=1&repo="+run.Repo, nil, &list); err != nil {
		return flow, 0, nil, fmt.Errorf("list runs: %w", err)
	}
	var audit []server.AuditEntry
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/audit?decision=deny,reject", nil, &audit); err != nil {
		return flow, 0, nil, fmt.Errorf("read the audit log: %w", err)
	}
	if dir == "" {
		return flow, 0, nil, errors.New("the run has no scratch directory")
	}
	// A head whose commits cannot be read goes unchecked rather than
	// costing the run its evidence.
	var tips []prTip
	for _, h := range heads {
		if slices.Contains(prs, h.Number) && h.Head.Sha != "" {
			if t, err := prTipOf(ctx, rc, h); err == nil {
				tips = append(tips, t)
			}
		}
	}
	var sts prStatuses
	for _, h := range heads {
		if slices.Contains(prs, h.Number) {
			sts.add(ctx, rc, h, list)
		}
	}
	n, gaps, err := gatherRetro(dir, flow, run.ID, rc.ws, list, audit, prs, tips, sts)
	return flow, n, gaps, err
}

// collectRetroWindow gathers the evidence of a windowed retro run (#383)
// into dir with the owner key: the flows of the pull requests merged by
// the default branch's first-parent commits after the window's base, up
// to the run's SHA, read in gitDir. It returns the scope it covered, how
// many runs it found and the gaps it saw.
func collectRetroWindow(ctx context.Context, rc *runContext, run secrets.Run, gitDir, dir string) (retroScope, int, []string, error) {
	scope := retroScope{Window: run.Window, Head: run.SHA}
	if dir == "" {
		return scope, 0, nil, errors.New("the run has no scratch directory")
	}
	revs, err := runs.GitCtx(ctx, gitDir, nil, "rev-list", "--first-parent", "--reverse", run.Window.Base+".."+run.SHA)
	if err != nil {
		return scope, 0, nil, fmt.Errorf("list the window's commits: %w", err)
	}
	heads, err := prHeads(ctx, rc)
	if err != nil {
		return scope, 0, nil, fmt.Errorf("list pull requests: %w", err)
	}
	var list []server.RunItem
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs?history=1&repo="+run.Repo, nil, &list); err != nil {
		return scope, 0, nil, fmt.Errorf("list runs: %w", err)
	}
	scope.Merged, scope.Flows = windowFlows(strings.Fields(revs), heads, list)
	var audit []server.AuditEntry
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/audit?decision=deny,reject", nil, &audit); err != nil {
		return scope, 0, nil, fmt.Errorf("read the audit log: %w", err)
	}
	var tips []prTip
	var sts prStatuses
	for _, h := range heads {
		if !windowHas(scope, h.Number) || h.Head.Sha == "" {
			continue
		}
		if t, err := prTipOf(ctx, rc, h); err == nil {
			tips = append(tips, t)
		}
		sts.add(ctx, rc, h, list)
	}
	n, gaps, err := gatherRetroScope(dir, scope, run.ID, rc.ws, list, audit, tips, sts)
	return scope, n, gaps, err
}

// windowFlows is the pull requests that the window's first-parent
// commits revs (oldest first) merged, in that order, and their flows,
// each once: an issue/<m> pull request stands for issue m's flow, with
// every pull request from its branch (heads). A merged pull request whose
// head branch is gone (most are, deleted after the merge) stands for the
// issue prIssue recovers from the runs in list or its title, and records it
// as its Issue. A milestone's integration pull request, from a
// milestone/<slug> branch, also brings the flows of the pull requests merged
// into that branch (#449): it records them as its Children, and their flows
// as the milestone's.
func windowFlows(revs []string, heads []prHead, list []server.RunItem) ([]prHead, []retroFlowPRs) {
	at := map[string]int{}
	for i, r := range revs {
		at[r] = i
	}
	var merged []prHead
	for _, h := range heads {
		if _, ok := at[h.MergeCommit]; ok && h.Merged {
			merged = append(merged, h)
		}
	}
	slices.SortStableFunc(merged, func(a, b prHead) int { return cmp.Compare(at[a.MergeCommit], at[b.MergeCommit]) })
	var flows []retroFlowPRs
	seen := map[string]int{}
	add := func(pr *prHead, milestone int64) {
		flow, prs := prFlow(pr, heads, list)
		k := flow.String()
		if j, ok := seen[k]; ok {
			if !slices.Contains(flows[j].ForgePRs, pr.Number) {
				flows[j].ForgePRs = append(flows[j].ForgePRs, pr.Number)
			}
			return
		}
		seen[k] = len(flows)
		flows = append(flows, retroFlowPRs{Flow: flow, ForgePRs: prs, Milestone: milestone})
	}
	for i := range merged {
		pr := &merged[i]
		add(pr, 0)
		if !strings.HasPrefix(pr.Head.Ref, server.MilestoneBranchPrefix) {
			continue
		}
		for _, h := range heads {
			if h.Merged && h.Base.Ref == pr.Head.Ref && h.Number != pr.Number {
				pr.Children = append(pr.Children, h)
			}
		}
		slices.SortFunc(pr.Children, func(a, b prHead) int { return cmp.Compare(a.Number, b.Number) })
		for j := range pr.Children {
			add(&pr.Children[j], pr.Number)
		}
	}
	return merged, flows
}

// prFlow is the flow merged pull request pr stands for in a window, and
// the pull requests from its branch: its issue's flow, whichever of the
// issue's pull requests merged, with the issue recovered (and recorded as
// pr.Issue) when its head branch is gone.
func prFlow(pr *prHead, heads []prHead, list []server.RunItem) (server.RetroFlow, []int64) {
	flow, prs := retroFlowOf(pr.Number, heads)
	if flow.Issue == 0 && branchGone(*pr) {
		if m := prIssue(*pr, list); m > 0 {
			pr.Issue = m
			flow, prs = server.NewRetroFlow(m, false, ""), []int64{pr.Number}
			for _, h := range heads {
				if h.Head.Ref == flow.Branch {
					prs = append(prs, h.Number)
				}
			}
		}
	}
	if flow.Issue > 0 {
		flow = server.NewRetroFlow(flow.Issue, false, "")
	}
	return flow, prs
}

// windowHas is whether pull request n is one of scope's flows'.
func windowHas(scope retroScope, n int64) bool {
	for _, f := range scope.Flows {
		if slices.Contains(f.ForgePRs, n) || f.Flow.PR == n {
			return true
		}
	}
	return false
}

// add reads the commit statuses at h's commits, its head, and the heads
// its verify runs saw (a force push may have dropped them from it). What it
// cannot read it records in Errors rather than costing the run its evidence.
func (s *prStatuses) add(ctx context.Context, rc *runContext, h prHead, list []server.RunItem) {
	shas, err := prCommits(ctx, rc, h.Number)
	if err != nil {
		s.Errors = append(s.Errors, fmt.Sprintf("The commits of pull request !%d could not be listed (%v): its commit statuses below are only those at the heads the runs saw.", h.Number, err))
	}
	shas = append(shas, h.Head.Sha)
	for _, it := range list {
		if it.Run.PR == h.Number { // a verify run, pinned to the head it saw
			shas = append(shas, it.Run.SHA)
		}
	}
	seen := map[string]bool{"": true}
	for _, sha := range shas {
		if seen[sha] {
			continue
		}
		seen[sha] = true
		got, err := commitStatuses(ctx, rc, sha)
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("The commit statuses at `%s` of pull request !%d could not be read: %v.", short(sha), h.Number, err))
			continue
		}
		for _, st := range got {
			st.PR = h.Number
			s.Statuses = append(s.Statuses, st)
		}
	}
}

// prCommits is the SHAs of pull request n's commits.
func prCommits(ctx context.Context, rc *runContext, n int64) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var cs []struct{ Sha string }
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/commits?stat=false&verification=false&files=false&limit=50&page=%d", rc.org, rc.repo, n, page)
		if err := callServer(ctx, rc.inst, rc.key, "GET", path, nil, &cs); err != nil {
			return out, err
		}
		for _, c := range cs {
			out = append(out, c.Sha)
		}
		if len(cs) < 50 {
			return out, nil
		}
	}
}

// commitStatuses is every status at sha, not only the newest per context.
func commitStatuses(ctx context.Context, rc *runContext, sha string) ([]prStatus, error) {
	var out []prStatus
	for page := 1; ; page++ {
		var sts []struct {
			Status      string
			Context     string
			Description string
			Created     time.Time `json:"created_at"`
			Creator     *struct{ Login string }
		}
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/commits/%s/statuses?limit=50&page=%d", rc.org, rc.repo, sha, page)
		if err := callServer(ctx, rc.inst, rc.key, "GET", path, nil, &sts); err != nil {
			return nil, err
		}
		for _, st := range sts {
			p := prStatus{SHA: sha, Context: st.Context, State: st.Status, Description: st.Description, Created: st.Created}
			if st.Creator != nil {
				p.Creator = st.Creator.Login
			}
			out = append(out, p)
		}
		if len(sts) < 50 {
			return out, nil
		}
	}
}

// prHead is a pull request's number, head branch and head commit, and,
// once merged, its title and merge commit (a windowed retro's selection).
type prHead struct {
	Number      int64
	Title       string
	Head        struct{ Ref, Sha string }
	Base        struct{ Ref string }
	Merged      bool
	MergeCommit string `json:"merge_commit_sha"`
	// Issue is the issue windowFlows recovered for a pull request whose
	// head branch is gone (prIssue); not Forgejo's.
	Issue int64 `json:"-"`
	// Children are, for a milestone's integration pull request, the pull
	// requests merged into its milestone/<slug> branch, by number
	// (windowFlows); not Forgejo's.
	Children []prHead `json:"-"`
}

// branchGone is whether Forgejo no longer has h's head branch: it then
// reports the pull request's own ref, refs/pull/N/head, as the head.
func branchGone(h prHead) bool {
	return h.Head.Ref == "" || strings.HasPrefix(h.Head.Ref, "refs/pull/")
}

// titleIssue matches the "(#N)" that names a pull request's issue in its
// title.
var titleIssue = regexp.MustCompile(`\(#([1-9][0-9]*)\)`)

// prIssue is the issue of pull request h, whose head branch is gone: the
// issue/<m> branch its runs (verify runs) record, or else the last "(#m)"
// in its title; 0 when neither names one.
func prIssue(h prHead, list []server.RunItem) int64 {
	for _, it := range list {
		if it.Run.PR == h.Number {
			if m, ok := server.IssueBranchNumber(it.Run.Branch); ok {
				return m
			}
		}
	}
	if ms := titleIssue.FindAllStringSubmatch(h.Title, -1); len(ms) > 0 {
		if m, err := strconv.ParseInt(ms[len(ms)-1][1], 10, 64); err == nil {
			return m
		}
	}
	return 0
}

// maxTipMerges bounds how many merge commits prTipOf walks down.
const maxTipMerges = 20

// isLandingCommit is whether a commit with this message and author name is
// a Land landing commit (server opPRLanding): "Merge !N (...) into <base>"
// by bottown, whose first parent is the base and second the PR's head.
func isLandingCommit(message, author string) bool {
	return author == "bottown" && strings.HasPrefix(message, "Merge !")
}

// prTipOf is h's head and its own last commit, below any merge commits:
// it follows a merge's first parent (the PR branch, in "Merge main into
// issue/N"), but a landing commit's second, the PR's head.
func prTipOf(ctx context.Context, rc *runContext, h prHead) (prTip, error) {
	sha := h.Head.Sha
	for i := 0; ; i++ {
		var c struct {
			Commit struct {
				Message string
				Author  struct{ Name string }
			}
			Author  *struct{ Login string }
			Parents []struct{ Sha string }
		}
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/git/commits/%s?stat=false&verification=false&files=false", rc.org, rc.repo, sha)
		if err := callServer(ctx, rc.inst, rc.key, "GET", path, nil, &c); err != nil {
			return prTip{}, err
		}
		if len(c.Parents) < 2 || i == maxTipMerges {
			author := c.Commit.Author.Name
			if c.Author != nil && c.Author.Login != "" {
				author = c.Author.Login
			}
			return prTip{PR: h.Number, Head: h.Head.Sha, SHA: sha, Author: author}, nil
		}
		if isLandingCommit(c.Commit.Message, c.Commit.Author.Name) {
			sha = c.Parents[1].Sha
		} else {
			sha = c.Parents[0].Sha
		}
	}
}

// prHeads is rc's repository's pull requests, open or closed.
func prHeads(ctx context.Context, rc *runContext) ([]prHead, error) {
	var out []prHead
	for page := 1; ; page++ {
		var prs []prHead
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=all&limit=50&page=%d", rc.org, rc.repo, page)
		if err := callServer(ctx, rc.inst, rc.key, "GET", path, nil, &prs); err != nil {
			return nil, err
		}
		out = append(out, prs...)
		if len(prs) < 50 {
			return out, nil
		}
	}
}

// retroFlowOf is the flow of a retro on n, given the repository's pull
// requests (heads): n's issue flow when n is a pull request from an
// issue/<m> branch, and the numbers of the pull requests from the flow's
// branch.
func retroFlowOf(n int64, heads []prHead) (server.RetroFlow, []int64) {
	flow := server.NewRetroFlow(n, false, "")
	for _, pr := range heads {
		if pr.Number == n {
			flow = server.NewRetroFlow(n, true, pr.Head.Ref)
		}
	}
	var prs []int64
	for _, pr := range heads {
		if flow.Branch != "" && pr.Head.Ref == flow.Branch {
			prs = append(prs, pr.Number)
		}
	}
	return flow, prs
}
