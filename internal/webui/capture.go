package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/runs"
)

// What a capture takes on its own, when it is not told which issues and
// runs: the ones the captured views link to, up to these many.
const (
	captureIssueCap = 30
	captureLogCap   = 20
)

// CaptureOptions says what a capture adds to the views: issue details and
// timelines, and rendered run logs. With neither, it takes the issues and
// runs the views link to, up to a cap.
type CaptureOptions struct {
	Issues []int64
	Logs   []string
}

// Capture computes what the web UI's API answers now, with the same code
// as its handlers, as a Fixture `bottown web preview` can serve. Its
// actions are empty: a preview action does nothing. The returned warnings
// name what the views link to but could not be read; an issue or log
// asked for by name that cannot be read is an error.
func (s *Server) Capture(ctx context.Context, opts CaptureOptions) (*Fixture, []string, error) {
	st, err := s.ComputeStatus(ctx, false)
	if err != nil {
		return nil, nil, fmt.Errorf("status: %w", err)
	}
	history, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("history: %w", err)
	}
	milestones, err := s.ComputeMilestones(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("milestones: %w", err)
	}
	status := *st
	fx := &Fixture{
		Inbox:       orEmpty(st.Inbox),
		LiveRuns:    orEmpty(st.LiveRuns),
		LiveChecks:  orEmpty(st.LiveChecks),
		ActiveFlows: orEmpty(st.ActiveFlows),
		History:     orEmpty(history),
		Milestones:  orEmpty(milestones),
		Logs:        map[string][]runs.LogLine{},
		Issues:      map[string]IssueDetails{},
		Timelines:   map[string]IssueTimeline{},
		Actions:     map[string]ActionResult{},
	}
	// The preview serves the fixture's own inbox, live runs and checks and
	// active flows in the status: keep one copy.
	status.Inbox, status.LiveRuns, status.LiveChecks, status.ActiveFlows = nil, nil, nil, nil
	fx.Status = status

	issues, prs, logs := opts.Issues, []int64(nil), opts.Logs
	named := len(issues) > 0 || len(logs) > 0
	if !named {
		issues, prs, logs = linked(fx)
	}
	var warnings []string
	fail := func(err error) error {
		if named {
			return err
		}
		warnings = append(warnings, err.Error())
		return nil
	}
	for _, n := range issues {
		d, err := s.ComputeIssueDetails(ctx, n)
		if err != nil {
			if err := fail(err); err != nil {
				return nil, nil, err
			}
			continue
		}
		tl, err := s.ComputeIssueTimeline(ctx, n)
		if err != nil {
			if err := fail(fmt.Errorf("timeline of #%d: %w", n, err)); err != nil {
				return nil, nil, err
			}
			continue
		}
		key := strconv.FormatInt(n, 10)
		fx.Issues[key], fx.Timelines[key] = *d, *tl
	}
	for _, pr := range prs {
		tl, err := s.ComputeFlowTimeline(ctx, 0, pr)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("timeline of !%d: %v", pr, err))
			continue
		}
		fx.Timelines["!"+strconv.FormatInt(pr, 10)] = *tl
	}
	for _, id := range logs {
		lines, ok, err := s.ComputeRunLog(ctx, id)
		if err == nil && !ok && named {
			err = fmt.Errorf("run %s has no log in %s", id, filepath.Join(s.cfg.RepoTop, ".bottown", "runs"))
		}
		if err != nil {
			return nil, nil, err
		}
		if ok {
			fx.Logs[id] = lines
		}
	}
	return fx, warnings, nil
}

// linked returns the issues, issue-less PRs and runs the views in fx link
// to, live work first, each up to its cap.
func linked(fx *Fixture) (issues, prs []int64, logs []string) {
	seen := map[int64]bool{}
	addIssue := func(n int64) {
		if n > 0 && !seen[n] && len(issues) < captureIssueCap {
			seen[n] = true
			issues = append(issues, n)
		}
	}
	seenPR := map[int64]bool{}
	addPR := func(issue, pr int64) {
		if issue == 0 && pr > 0 && !seenPR[pr] && len(prs) < captureIssueCap {
			seenPR[pr] = true
			prs = append(prs, pr)
		}
	}
	seenLog := map[string]bool{}
	addLog := func(id string) {
		if id != "" && !seenLog[id] && len(logs) < captureLogCap {
			seenLog[id] = true
			logs = append(logs, id)
		}
	}
	for _, r := range fx.LiveRuns {
		addIssue(r.Issue)
		addPR(r.Issue, r.PR)
		addLog(r.ID)
	}
	for _, c := range fx.LiveChecks {
		addIssue(c.Issue)
		addLog(c.ID)
	}
	for _, f := range fx.ActiveFlows {
		addIssue(f.Issue)
	}
	for _, it := range fx.Inbox {
		addIssue(it.Issue)
	}
	for _, g := range fx.Milestones {
		for _, ref := range []*IssueRef{g.Kickoff, g.Epic} {
			if ref != nil {
				addIssue(ref.Number)
			}
		}
		for _, n := range g.Nodes {
			addIssue(n.Number)
		}
	}
	for _, h := range fx.History {
		addIssue(h.Issue)
		addPR(h.Issue, h.PR)
		if !h.Purged {
			addLog(h.ID)
		}
	}
	return issues, prs, logs
}

// What a capture drops because it could be a secret: bottown API keys (a
// run's or the owner's), the one-time logins of `bottown web ui` and
// `bottown web forgejo`, and the credentials in a URL. The replacements
// have no quotes or backslashes, so JSON stays valid.
var (
	scrubKey      = regexp.MustCompile(`btk_[A-Za-z0-9_-]{16,}`)
	scrubTicket   = regexp.MustCompile(`/v1/web/login/[A-Za-z0-9_-]+`)
	scrubToken    = regexp.MustCompile(`([?&])token=[^&\s"\\]+`)
	scrubUserinfo = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)[^/\s"\\@]+@`)
)

// scrub removes what could be a secret from a fixture's JSON, and the
// owner key wherever it appears.
func scrub(b []byte, ownerKey string) []byte {
	if ownerKey != "" {
		b = bytes.ReplaceAll(b, []byte(ownerKey), []byte("[owner key elided by bottown]"))
	}
	b = scrubKey.ReplaceAll(b, []byte("btk_[elided by bottown]"))
	b = scrubTicket.ReplaceAll(b, []byte("/v1/web/login/[elided by bottown]"))
	b = scrubToken.ReplaceAll(b, []byte("${1}token=[elided by bottown]"))
	b = scrubUserinfo.ReplaceAll(b, []byte("${1}"))
	return runs.RedactEncoded(b)
}

// EncodeFixture encodes fx as a fixture file, with what could be a secret
// scrubbed (see scrub), and checks that it loads back strictly.
func (s *Server) EncodeFixture(fx *Fixture) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep & in URLs, for the scrubbing and the reader
	enc.SetIndent("", "  ")
	if err := enc.Encode(fx); err != nil {
		return nil, err
	}
	b := scrub(buf.Bytes(), s.cfg.OwnerKey)
	if _, err := decodeFixture(b); err != nil {
		return nil, fmt.Errorf("the capture does not load back: %w", err)
	}
	return b, nil
}

// ErrFixtureExists is WriteFixture's refusal to replace a fixture.
var ErrFixtureExists = errors.New("fixture exists")

// WriteFixture writes b as the fixture name in dir and returns its path. It
// refuses to replace one unless force.
func WriteFixture(dir, name string, b []byte, force bool) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("invalid fixture name %q", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".json")
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%w: %s (--force replaces it)", ErrFixtureExists, path)
	}
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

// Summary counts what fx holds, per section.
func (fx *Fixture) Summary() string {
	return fmt.Sprintf("%d inbox, %d live runs, %d live checks, %d active flows, %d history, %d milestones, %d issues, %d timelines, %d logs",
		len(fx.Inbox), len(fx.LiveRuns), len(fx.LiveChecks), len(fx.ActiveFlows), len(fx.History), len(fx.Milestones),
		len(fx.Issues), len(fx.Timelines), len(fx.Logs))
}
