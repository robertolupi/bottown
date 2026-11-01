package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/secrets"
)

type runsFixture struct {
	mu       sync.Mutex
	actors   []ActorDetail
	runs     []secrets.Run
	prs      []*sdk.PullRequest
	reviews  map[int64][]*sdk.PullReview
	issues   []*sdk.Issue
	comments map[int64][]*sdk.Comment
	denials  []Denial // what GET /v1/audit?decision=deny returns
}

func newRunsFixtureServer(t *testing.T, fix *runsFixture) (*httptest.Server, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fix.mu.Lock()
		defer fix.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path

		switch {
		case p == "/v1/actors":
			_ = json.NewEncoder(w).Encode(fix.actors)
		case p == "/v1/runs":
			state := r.URL.Query().Get("state")
			history := r.URL.Query().Get("history")
			var filtered []secrets.Run
			now := time.Now().UTC()
			for _, run := range fix.runs {
				if state == "live" && (run.Ended != nil || !run.Active(now)) {
					continue
				}
				if state == "ended" && run.Ended == nil {
					continue
				}
				if history != "1" && run.Purged {
					continue
				}
				filtered = append(filtered, run)
			}
			_ = json.NewEncoder(w).Encode(filtered)
		case p == "/v1/audit":
			// Refusals are selected by run: a repo filter would drop a
			// run's out-of-repo and repo-less ones.
			if q := r.URL.Query(); q.Get("decision") != "deny,reject" || q.Has("repo") {
				http.Error(w, "want decision=deny,reject and no repo", http.StatusBadRequest)
				return
			}
			out := fix.denials
			if out == nil {
				out = []Denial{}
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, "/v1/runs/"):
			id := strings.TrimPrefix(p, "/v1/runs/")
			for _, run := range fix.runs {
				if run.ID == id {
					_ = json.NewEncoder(w).Encode(run)
					return
				}
			}
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/pulls"):
			_ = json.NewEncoder(w).Encode(fix.prs)
		case strings.Contains(p, "/pulls/") && strings.HasSuffix(p, "/reviews"):
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
			revs := fix.reviews[num]
			if revs == nil {
				revs = []*sdk.PullReview{}
			}
			_ = json.NewEncoder(w).Encode(paginate(r, revs))
		case strings.Contains(p, "/pulls/"):
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			for _, pr := range fix.prs {
				if pr.Index == num {
					_ = json.NewEncoder(w).Encode(pr)
					return
				}
			}
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/issues"):
			_ = json.NewEncoder(w).Encode(fix.issues)
		case strings.Contains(p, "/issues/") && strings.HasSuffix(p, "/comments"):
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
			cmts := fix.comments[num]
			if cmts == nil {
				cmts = []*sdk.Comment{}
			}
			_ = json.NewEncoder(w).Encode(paginate(r, cmts))
		case strings.Contains(p, "/issues/"):
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			for _, iss := range fix.issues {
				if iss.Index == num {
					_ = json.NewEncoder(w).Encode(iss)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, port
}

func defaultRunsFixture() *runsFixture {
	return &runsFixture{
		actors: []ActorDetail{
			{Name: "claude", Family: "anthropic", Harness: "claude"},
			{Name: "agy", Family: "google", Harness: "agy"},
			{Name: "kimi", Family: "moonshot", Harness: "kimi"},
		},
		reviews:  make(map[int64][]*sdk.PullReview),
		comments: make(map[int64][]*sdk.Comment),
	}
}

func TestRuns_LiveRunsAndFlowGrouping(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()

	// 1 open PR for issue/51 with one REQUEST_CHANGES review (so round 2)
	fix.prs = []*sdk.PullRequest{
		{
			Index: 52,
			Title: "PR for 51",
			State: sdk.StateOpen,
			Head:  &sdk.PRBranchInfo{Ref: "issue/51", Sha: "11112222"},
		},
	}
	fix.reviews[52] = []*sdk.PullReview{
		{
			ID:        1,
			State:     sdk.ReviewStateRequestChanges,
			Reviewer:  &sdk.User{UserName: "kimi"},
			Submitted: now.Add(-30 * time.Minute),
		},
	}

	// Active verify run on PR 52
	fix.runs = []secrets.Run{
		{
			ID:          "r-verify-1",
			Kind:        "verify",
			Actor:       "kimi",
			Family:      "moonshot",
			Harness:     "kimi",
			Repo:        "bottown",
			PR:          52,
			Created:     now.Add(-5 * time.Minute),
			Expires:     now.Add(55 * time.Minute),
			Interactive: false,
		},
	}

	fix.denials = []Denial{
		{Time: now, Run: "r-verify-1", Op: "api", Method: "POST", Path: "/repos/bottown/bottown/issues/51/dependencies", Rule: "issue-dependencies"},
		{Time: now, Run: "r-other", Op: "git", Rule: "run-push"},
		{Time: now, Run: "r-verify-1", Op: "api", Method: "POST", Path: "/repos/bottown/other/issues/1/comments", Rule: "run-repo"},
		{Time: now, Run: "r-verify-1", Op: "api", Method: "POST", Path: "/forgejo/api/v1/repos/bottown/bottown/issues/51%2Fcomments", Rule: "unclean-path"},
	}
	_, port := newRunsFixtureServer(t, fix)

	top := t.TempDir()
	runsDir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Write log file for live run
	logContent := `{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"Bash","arguments":"{\"command\":\"make check\"}"}}]}
{"role":"assistant","content":"Running checks now"}
`
	if err := os.WriteFile(filepath.Join(runsDir, "r-verify-1.log"), []byte(logContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Active flow record for issue 51
	flowRec := fmt.Sprintf(`{"issue":51,"implement":"agy","verify":"kimi","rounds":3,"pid":%d}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(top, ".bottown", "flow-51.json"), []byte(flowRec), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
			OwnerLogin: "rlupi",
		},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	live, err := s.ComputeLiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(live) != 1 {
		t.Fatalf("expected 1 live run, got %d", len(live))
	}

	r := live[0]
	if r.ID != "r-verify-1" || r.Kind != "verify" || r.Actor != "kimi" {
		t.Fatalf("unexpected run info: %+v", r)
	}
	if r.Flow == nil {
		t.Fatal("expected run to be grouped under flow")
	}
	if r.Flow.Issue != 51 || r.Flow.Round != 2 {
		t.Fatalf("expected flow #51 round 2, got: %+v", r.Flow)
	}
	if !strings.Contains(r.Flow.Label, "#51 · round 2/3 · verifying with kimi") {
		t.Fatalf("unexpected flow label: %q", r.Flow.Label)
	}
	if len(r.LogTail) != 2 || r.LogTail[0] != "Bash: make check" || r.LogTail[1] != "Running checks now" {
		t.Fatalf("unexpected log tail: %+v", r.LogTail)
	}
	// The run's refusals outside its repository and with no repository
	// are its own too.
	if len(r.Denials) != 3 || r.Denials[0].Rule != "issue-dependencies" ||
		r.Denials[1].Path != "/repos/bottown/other/issues/1/comments" || r.Denials[2].Rule != "unclean-path" {
		t.Fatalf("expected the run's three refusals, got %+v", r.Denials)
	}
}

// deadPID returns the PID of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestRuns_ActiveFlows(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()

	// Issue 51 has a live verify run on PR 52. Issue 60 is between runs:
	// its PR 61 has two REQUEST_CHANGES reviews, one dismissed, which
	// both count, as in flow.countRounds, and reviews of other states,
	// which do not.
	fix.prs = []*sdk.PullRequest{
		{Index: 52, State: sdk.StateOpen, Head: &sdk.PRBranchInfo{Ref: "issue/51", Sha: "11112222"}},
		{Index: 61, State: sdk.StateOpen, Head: &sdk.PRBranchInfo{Ref: "issue/60", Sha: "33334444"}},
	}
	fix.reviews[52] = []*sdk.PullReview{
		{ID: 1, State: sdk.ReviewStateRequestChanges, Submitted: now.Add(-30 * time.Minute)},
	}
	fix.reviews[61] = []*sdk.PullReview{
		{ID: 2, State: sdk.ReviewStateRequestChanges, Dismissed: true, Submitted: now.Add(-50 * time.Minute)},
		{ID: 3, State: sdk.ReviewStateComment, Submitted: now.Add(-40 * time.Minute)},
		{ID: 4, State: sdk.ReviewStateRequestChanges, Submitted: now.Add(-20 * time.Minute)},
		{ID: 5, State: sdk.ReviewStateApproved, Dismissed: true, Submitted: now.Add(-10 * time.Minute)},
	}
	fix.runs = []secrets.Run{
		{
			ID: "r-verify-1", Kind: "verify", Actor: "kimi", Family: "moonshot", Harness: "kimi",
			Repo: "bottown", PR: 52, Created: now.Add(-5 * time.Minute), Expires: now.Add(55 * time.Minute),
		},
	}
	_, port := newRunsFixtureServer(t, fix)

	top := t.TempDir()
	dir := filepath.Join(top, ".bottown")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFlow := func(issue int64, rounds, pid int) string {
		t.Helper()
		p := filepath.Join(dir, fmt.Sprintf("flow-%d.json", issue))
		rec := fmt.Sprintf(`{"issue":%d,"implement":"agy","verify":"kimi","rounds":%d,"pid":%d}`, issue, rounds, pid)
		if err := os.WriteFile(p, []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	writeFlow(51, 3, os.Getpid())
	writeFlow(60, 4, os.Getpid())
	stale := writeFlow(70, 3, deadPID(t))
	// A record with no PID is malformed: it is not active, and stopping
	// it must not signal kill(-0), the UI's own process group.
	noPID := writeFlow(80, 3, 0)

	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	live, err := s.ComputeLiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	flows := s.ComputeActiveFlows(ctx, live)
	if len(flows) != 2 {
		t.Fatalf("expected flows for #51 and #60, got %+v", flows)
	}

	f := flows[0]
	if f.Issue != 51 || f.Waiting || f.Implement != "agy" || f.Verify != "kimi" ||
		f.Round != 2 || f.MaxRounds != 3 || f.Step != "verifying with kimi" ||
		f.Label != "#51 · round 2/3 · verifying with kimi" {
		t.Fatalf("flow #51 should take its label from the live run, got %+v", f)
	}

	f = flows[1]
	if f.Issue != 60 || !f.Waiting || f.Round != 3 || f.MaxRounds != 4 ||
		f.Step != "waiting between rounds" || f.Label != "#60 · round 3/4 · waiting between rounds" {
		t.Fatalf("flow #60 should be waiting in round 3/4, got %+v", f)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale flow-70.json should be removed, stat err=%v", err)
	}

	if _, err := s.ExecuteStop(ctx, "flow-80"); err == nil || !strings.Contains(err.Error(), "no active flow for issue #80") {
		t.Fatalf("stopping a flow without a PID should fail, got err=%v", err)
	}
	if _, err := os.Stat(noPID); err != nil {
		t.Fatalf("flow-80.json should be left alone, stat err=%v", err)
	}
}

func TestRuns_HistoryRunsAndFilters(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()

	t1 := now.Add(-2 * time.Hour)
	t2 := now.Add(-1 * time.Hour)
	dur1 := int64(120000)

	ended1 := now.Add(-110 * time.Minute)
	ended2 := now.Add(-50 * time.Minute)

	fix.runs = []secrets.Run{
		{
			ID:      "r-ended-1",
			Kind:    "implement",
			Actor:   "claude",
			Family:  "anthropic",
			Repo:    "bottown",
			Issue:   10,
			Created: t1,
			Ended:   &ended1,
			Status:  "succeeded",
			Digest: secrets.Digest{
				Summary: "Fixed bug in web UI\nDetailed summary here",
				Usage: &secrets.Usage{
					DurationMS: &dur1,
				},
			},
		},
		{
			ID:      "r-ended-2",
			Kind:    "verify",
			Actor:   "agy",
			Family:  "google",
			Repo:    "bottown",
			PR:      11,
			Created: t2,
			Ended:   &ended2,
			Status:  "failed",
			Digest: secrets.Digest{
				Summary: "Checks failed\nDetails here",
			},
		},
	}

	fix.denials = []Denial{
		{Time: t2, Run: "r-ended-2", Op: "review_submit", Detail: "head moved to abc"},
	}
	_, port := newRunsFixtureServer(t, fix)
	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
			OwnerLogin: "rlupi",
		},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. All history
	all, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 history runs, got %d", len(all))
	}
	if all[0].Summary != "Fixed bug in web UI" && all[1].Summary != "Fixed bug in web UI" {
		t.Fatalf("expected first line summary 'Fixed bug in web UI', got %+v", all)
	}
	for _, h := range all {
		want := 0
		if h.ID == "r-ended-2" {
			want = 1
		}
		if h.Denials == nil || len(h.Denials) != want {
			t.Fatalf("run %s: denials %+v, want %d", h.ID, h.Denials, want)
		}
	}

	// 2. Filter by actor claude
	claudeRuns, _ := s.ComputeHistoryRuns(ctx, HistoryFilter{Actor: "claude"})
	if len(claudeRuns) != 1 || claudeRuns[0].Actor != "claude" {
		t.Fatalf("expected 1 claude run, got %+v", claudeRuns)
	}

	// 3. Filter by status failed
	failedRuns, _ := s.ComputeHistoryRuns(ctx, HistoryFilter{Status: "failed"})
	if len(failedRuns) != 1 || failedRuns[0].Status != "failed" {
		t.Fatalf("expected 1 failed run, got %+v", failedRuns)
	}
}

func TestRuns_IssueTimeline(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()

	issCreated := now.Add(-3 * time.Hour)
	fix.issues = []*sdk.Issue{
		{
			Index:   20,
			Title:   "Issue 20 Title",
			State:   sdk.StateClosed,
			Poster:  &sdk.User{UserName: "rlupi"},
			Created: issCreated,
		},
	}

	prCreated := now.Add(-2 * time.Hour)
	prMerged := now.Add(-30 * time.Minute)
	fix.prs = []*sdk.PullRequest{
		{
			Index:     21,
			Title:     "PR 21 Title",
			State:     sdk.StateClosed,
			Poster:    &sdk.User{UserName: "agy"},
			Head:      &sdk.PRBranchInfo{Ref: "issue/20", Sha: "22223333"},
			Created:   &prCreated,
			HasMerged: true,
			Merged:    &prMerged,
			MergedBy:  &sdk.User{UserName: "rlupi"},
		},
	}

	fix.reviews[21] = []*sdk.PullReview{
		{
			ID:        1,
			State:     sdk.ReviewStateApproved,
			Reviewer:  &sdk.User{UserName: "claude"},
			Submitted: now.Add(-40 * time.Minute),
		},
	}

	endedRun := now.Add(-100 * time.Minute)
	fix.runs = []secrets.Run{
		{
			ID:      "r-20-1",
			Kind:    "implement",
			Actor:   "agy",
			Repo:    "bottown",
			Issue:   20,
			Created: now.Add(-150 * time.Minute),
			Ended:   &endedRun,
			Status:  "succeeded",
			Digest:  secrets.Digest{Summary: "Opened PR !21"},
		},
	}

	_, port := newRunsFixtureServer(t, fix)
	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
			OwnerLogin: "rlupi",
		},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	timeline, err := s.ComputeIssueTimeline(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}

	if timeline.Issue != 20 || timeline.Title != "Issue 20 Title" {
		t.Fatalf("unexpected timeline header: %+v", timeline)
	}

	// Verify events in chronological order
	types := make([]string, len(timeline.Events))
	for i, ev := range timeline.Events {
		types[i] = ev.Type
	}

	expectedTypes := []string{"issue_created", "run", "pr_opened", "review", "pr_merged"}
	for _, exp := range expectedTypes {
		found := false
		for _, t := range types {
			if t == exp {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected timeline to contain event %q, got: %v", exp, types)
		}
	}
}

func TestActions_FlowCrossFamilyValidation(t *testing.T) {
	fix := defaultRunsFixture()
	// Two actors in the same family
	fix.actors = []ActorDetail{
		{Name: "claude-1", Family: "anthropic"},
		{Name: "claude-2", Family: "anthropic"},
		{Name: "agy", Family: "google"},
	}

	_, port := newRunsFixtureServer(t, fix)
	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
			OwnerLogin: "rlupi",
		},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Same-family verifier refused upfront
	_, err = s.ExecuteFlow(ctx, FlowRequest{
		Issue:     50,
		Implement: "claude-1",
		Verify:    "claude-2", // Same family!
	})
	if err == nil || !strings.Contains(err.Error(), "same model family") {
		t.Fatalf("expected same family error, got: %v", err)
	}

	// 2. Different family accepted
	// Note: will fail to run binary if not found, but must pass validation
	fakeScript := filepath.Join(cfg.RepoTop, "fake_bottown.sh")
	_ = os.WriteFile(fakeScript, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	s.cfg.BottownPath = fakeScript

	out, err := s.ExecuteFlow(ctx, FlowRequest{
		Issue:     50,
		Implement: "claude-1",
		Verify:    "agy", // Different family!
	})
	if err != nil {
		t.Fatalf("unexpected error starting flow: %v", err)
	}
	if !strings.Contains(out, "Flow started for issue #50") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestActions_StopAndGC(t *testing.T) {
	top := t.TempDir()
	fakeScript := filepath.Join(top, "fake_bottown.sh")
	script := `#!/bin/sh
if [ "$1" = "-i" ] && [ "$3" = "run" ] && [ "$4" = "stop" ]; then
    echo "run $5 stopped"
    exit 0
fi
if [ "$1" = "-i" ] && [ "$3" = "run" ] && [ "$4" = "gc" ]; then
    echo "cleaned up runs"
    exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: 3031,
			OwnerLogin: "rlupi",
		},
		RepoTop:     top,
		FullRepo:    "bottown/bottown",
		BottownPath: fakeScript,
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()

	// Stop
	out, err := s.ExecuteStop(ctx, "r-test-1")
	if err != nil || !strings.Contains(out, "run r-test-1 stopped") {
		t.Fatalf("expected run stopped, got out=%q, err=%v", out, err)
	}

	// GC
	out, err = s.ExecuteGC(ctx, false)
	if err != nil || !strings.Contains(out, "cleaned up runs") {
		t.Fatalf("expected cleaned up runs, got out=%q, err=%v", out, err)
	}
}

func TestEndpoints_RunsAndActions(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()
	fix.runs = []secrets.Run{
		{
			ID:          "r-live-api",
			Kind:        "implement",
			Actor:       "agy",
			Family:      "google",
			Repo:        "bottown",
			Issue:       42,
			Created:     now.Add(-10 * time.Minute),
			Expires:     now.Add(50 * time.Minute),
			Interactive: false,
		},
	}
	fix.issues = []*sdk.Issue{
		{
			Index:   42,
			Title:   "Issue 42",
			State:   sdk.StateOpen,
			Poster:  &sdk.User{UserName: "rlupi"},
			Created: now.Add(-1 * time.Hour),
		},
	}

	_, port := newRunsFixtureServer(t, fix)
	top := t.TempDir()
	runsDir := filepath.Join(top, ".bottown", "runs")
	_ = os.MkdirAll(runsDir, 0o700)
	_ = os.WriteFile(filepath.Join(runsDir, "r-live-api.log"), []byte("starting task\ntask done\n"), 0o600)

	fakeScript := filepath.Join(top, "fake_bottown.sh")
	_ = os.WriteFile(fakeScript, []byte("#!/bin/sh\necho \"fake ok\"\nexit 0\n"), 0o755)

	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
			OwnerLogin: "rlupi",
		},
		RepoTop:     top,
		FullRepo:    "bottown/bottown",
		BottownPath: fakeScript,
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx) }()

	time.Sleep(50 * time.Millisecond)

	cookie := &http.Cookie{
		Name:  s.cookieName,
		Value: s.sessionToken,
	}
	client := &http.Client{}

	doReq := func(method, path string, body string) (*http.Response, string) {
		url := fmt.Sprintf("http://127.0.0.1:%d%s", s.Port(), path)
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", s.Port()))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return resp, string(bodyBytes)
	}

	// 1. GET /api/runs?state=live
	resp, body := doReq(http.MethodGet, "/api/runs?state=live", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "r-live-api") {
		t.Fatalf("GET /api/runs?state=live: status %d, body %s", resp.StatusCode, body)
	}

	// 2. GET /api/runs/{id}/log
	resp, body = doReq(http.MethodGet, "/api/runs/r-live-api/log", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "starting task") {
		t.Fatalf("GET /api/runs/r-live-api/log: status %d, body %s", resp.StatusCode, body)
	}

	// Path traversal attempt should be rejected with 400 Bad Request
	resp, body = doReq(http.MethodGet, "/api/runs/..%2F..%2F..%2Fetc%2Ffoo/log", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal in log ID, got %d: %s", resp.StatusCode, body)
	}

	// 3. GET /api/issues/{number}/timeline
	resp, body = doReq(http.MethodGet, "/api/issues/42/timeline", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Issue 42") {
		t.Fatalf("GET /api/issues/42/timeline: status %d, body %s", resp.StatusCode, body)
	}

	// 4. POST /api/actions/flow - same family refused (400)
	resp, body = doReq(http.MethodPost, "/api/actions/flow", `{"issue":42,"implement":"claude","verify":"claude"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "same model family") {
		t.Fatalf("expected 400 for same family flow, got %d: %s", resp.StatusCode, body)
	}

	// 5. POST /api/actions/flow - different family accepted (200)
	resp, body = doReq(http.MethodPost, "/api/actions/flow", `{"issue":42,"implement":"claude","verify":"agy"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Flow started") {
		t.Fatalf("expected 200 for flow start, got %d: %s", resp.StatusCode, body)
	}

	// 6. POST /api/actions/run
	resp, body = doReq(http.MethodPost, "/api/actions/run", `{"kind":"implement","target":42,"actor":"agy"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Run (implement) started") {
		t.Fatalf("expected 200 for run start, got %d: %s", resp.StatusCode, body)
	}

	// 7. POST /api/actions/stop
	resp, body = doReq(http.MethodPost, "/api/actions/stop", `{"id":"r-live-api"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for stop, got %d: %s", resp.StatusCode, body)
	}

	// 8. POST /api/actions/gc
	resp, body = doReq(http.MethodPost, "/api/actions/gc", `{"failed":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for gc, got %d: %s", resp.StatusCode, body)
	}
}

func TestHistoryTableScrollStyles(t *testing.T) {
	// The built bundle is not tracked (dist/ui), so check the source.
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "App.svelte"))
	if err != nil {
		t.Fatalf("read App.svelte: %v", err)
	}
	src := string(data)
	for _, want := range []string{`class="table-container history-table-container"`, ".history-table-container {", "max-height: 440px", "max-block-size: 440px", "overflow: auto", "position: sticky"} {
		if !strings.Contains(src, want) {
			t.Errorf("expected App.svelte to contain %q", want)
		}
	}
}
