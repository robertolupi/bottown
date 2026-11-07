package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// A retro run's evidence: what bottown recorded of the flow it looks back
// on, gathered by the launcher with the owner key (the run's own key may
// not list runs or read the audit log) into the run's scratch directory:
//
//	retro/README.md                 the runs at a glance, and the files
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
	// Local is the launcher's record, when this checkout launched the run.
	Local *retroLocal `json:"local,omitempty"`
	// Denials are the policy refusals the server kept for the run.
	Denials []server.AuditEntry `json:"denials,omitempty"`
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

// inRetro is whether run r belongs to the flow of issue n: it worked the
// issue (implement, plan, discuss), or a pull request on its issue/<n>
// branch (verify), or n is that pull request.
func inRetro(r secrets.Run, n int64) bool {
	return r.Issue == n || r.PR == n || r.Branch == fmt.Sprintf("issue/%d", n)
}

// gatherRetro writes the evidence of issue n's flow, for retro run self,
// into dir, and returns how many runs it found. Local files come from ws;
// the server's records from runList (GET /v1/runs?history=1) and the
// denials from audit (GET /v1/audit?decision=deny,reject).
func gatherRetro(dir string, n int64, self string, ws *runs.Workspace, runList []server.RunItem, audit []server.AuditEntry) (int, error) {
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	for _, d := range []string{dir, filepath.Join(dir, "transcripts"), filepath.Join(dir, "prompts")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return 0, err
		}
	}
	local := map[string]runs.Record{}
	if recs, err := ws.Records(io.Discard); err == nil {
		for _, r := range recs {
			local[r.ID] = r
		}
	}
	var out []retroRun
	seen := map[string]bool{}
	add := func(r secrets.Run, state string) {
		if r.ID == self || seen[r.ID] || !inRetro(r, n) {
			return
		}
		seen[r.ID] = true
		rr := retroRun{
			ID: r.ID, Kind: r.Kind, Actor: r.Actor, Family: r.Family, Harness: r.Harness, Spec: r.Spec,
			Issue: r.Issue, PR: r.PR, Branch: r.Branch, SHA: r.SHA, Interactive: r.Interactive,
			AllowSameFamily: r.AllowSameFamily, StartedBy: r.StartedBy, Created: r.Created, Ended: r.Ended,
			State: state, Status: r.Status, Outcome: r.Outcome, Digest: r.Digest, Purged: r.Purged,
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
		}
		out = append(out, rr)
	}
	for _, it := range runList {
		add(it.Run, it.State)
	}
	// Runs the server no longer lists, but this checkout still records.
	for _, l := range local {
		add(secrets.Run{
			ID: l.ID, Kind: l.Kind, Actor: l.Actor, Family: l.Family, Harness: l.Harness, Spec: l.Spec,
			Issue: l.Issue, PR: l.PR, Branch: l.Branch, SHA: l.SHA, Interactive: l.Interactive,
			Created: l.Started, Ended: l.Ended,
		}, "")
	}
	slices.SortFunc(out, func(a, b retroRun) int { return a.Created.Compare(b.Created) })

	for i := range out {
		r := &out[i]
		if b, err := runs.ReadLog(ws.Path(r.ID, ".log")); err == nil && len(b) > 0 {
			rel := filepath.Join("transcripts", r.ID+".jsonl")
			if err := writeTranscript(filepath.Join(dir, rel), r.Harness, b); err != nil {
				return 0, err
			}
			r.Transcript = rel
		}
		if b, err := os.ReadFile(ws.Path(r.ID, ".prompt.md")); err == nil {
			rel := filepath.Join("prompts", r.ID+".md")
			if err := os.WriteFile(filepath.Join(dir, rel), b, 0o600); err != nil {
				return 0, err
			}
			r.Prompt = rel
		}
	}
	b, err := json.MarshalIndent(orEmptyRuns(out), "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), append(b, '\n'), 0o600); err != nil {
		return 0, err
	}
	return len(out), os.WriteFile(filepath.Join(dir, "README.md"), []byte(retroReadme(n, out)), 0o600)
}

func orEmptyRuns(rs []retroRun) []retroRun {
	if rs == nil {
		return []retroRun{}
	}
	return rs
}

// writeTranscript writes a run's log as JSON lines of runs.LogLine, each
// text field bounded by maxTranscriptField.
func writeTranscript(path, harness string, log []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, l := range runs.RenderLog(harness, log) {
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
	return err
}

// retroReadme is the evidence's index: the runs, oldest first, and where
// to read more.
func retroReadme(n int64, rs []retroRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence for the retro of #%d\n\n", n)
	if len(rs) == 0 {
		b.WriteString("bottown has no record of a run on this issue or its pull requests.\n")
		return b.String()
	}
	b.WriteString("The runs that worked the issue or its pull requests, oldest first. `runs.json`\n" +
		"has each run in full: the server's record and digest (summary, usage, observed failure),\n" +
		"the launcher's record, and the policy refusals (denials). Transcripts are JSON lines,\n" +
		"one per assistant message, tool call or status event, the same format for every harness:\n" +
		"`kind` (text, tool, output, error, status, plain), `tool`, `state`, `text`, `parameters`, `output`.\n\n")
	b.WriteString("| run | kind | actor | target | created | duration | status | outcome | denials | transcript | prompt |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
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
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %d | %s | %s |\n",
			r.ID, r.Kind, actor, target, r.Created.Format(time.RFC3339), orDash(r.Duration), status,
			orDash(r.Outcome), len(r.Denials), transcript, orDash(r.Prompt))
	}
	return b.String()
}

// collectRetro gathers the evidence for retro run run into dir with the
// owner key, and returns how many runs it found.
func collectRetro(ctx context.Context, rc *runContext, run secrets.Run, dir string) (int, error) {
	var list []server.RunItem
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs?history=1&repo="+run.Repo, nil, &list); err != nil {
		return 0, fmt.Errorf("list runs: %w", err)
	}
	var audit []server.AuditEntry
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/audit?decision=deny,reject", nil, &audit); err != nil {
		return 0, fmt.Errorf("read the audit log: %w", err)
	}
	if dir == "" {
		return 0, errors.New("the run has no scratch directory")
	}
	return gatherRetro(dir, run.Issue, run.ID, rc.ws, list, audit)
}
