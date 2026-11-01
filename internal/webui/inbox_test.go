package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

type fixtureForge struct {
	mu       sync.Mutex
	actors   []map[string]any
	whoami   map[string]any
	runs     []secrets.Run
	prs      []*sdk.PullRequest
	reviews  map[int64][]*sdk.PullReview
	issues   []*sdk.Issue
	comments map[int64][]*sdk.Comment
}

func newFixtureServer(t *testing.T, fix *fixtureForge) (*httptest.Server, int) {
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
		case p == "/v1/whoami":
			_ = json.NewEncoder(w).Encode(fix.whoami)
		case p == "/v1/runs":
			_ = json.NewEncoder(w).Encode(fix.runs)
		case strings.HasSuffix(p, "/pulls"):
			_ = json.NewEncoder(w).Encode(paginate(r, fix.prs))
		case strings.Contains(p, "/pulls/") && strings.HasSuffix(p, "/reviews"):
			// /forgejo/api/v1/repos/{org}/{repo}/pulls/{number}/reviews
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
			revs := fix.reviews[num]
			if revs == nil {
				revs = []*sdk.PullReview{}
			}
			_ = json.NewEncoder(w).Encode(paginate(r, revs))
		case strings.HasSuffix(p, "/issues"):
			_ = json.NewEncoder(w).Encode(paginate(r, fix.issues))
		case strings.Contains(p, "/issues/") && strings.HasSuffix(p, "/comments"):
			// /forgejo/api/v1/repos/{org}/{repo}/issues/{number}/comments
			parts := strings.Split(p, "/")
			num, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
			cmts := fix.comments[num]
			if cmts == nil {
				cmts = []*sdk.Comment{}
			}
			_ = json.NewEncoder(w).Encode(cmts)
		case p == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "dev"})
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, port
}

// paginate returns the slice of items a Forgejo list endpoint would send for
// the request's page/limit, mirroring the real server's windowing.
func paginate[T any](r *http.Request, items []T) []T {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	start := (page - 1) * limit
	if start >= len(items) {
		return []T{}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func defaultFixture() *fixtureForge {
	return &fixtureForge{
		actors: []map[string]any{
			{"name": "claude", "family": "anthropic", "harness": "claude"},
			{"name": "agy", "family": "google", "harness": "agy"},
			{"name": "kimi", "family": "moonshot", "harness": "kimi"},
		},
		whoami: map[string]any{
			"actor":    "rlupi",
			"instance": "prod",
		},
		reviews:  make(map[int64][]*sdk.PullReview),
		comments: make(map[int64][]*sdk.Comment),
	}
}

func TestInbox_ReadyToMerge(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	sha1 := "1111222233334444555566667777888899990000"
	now := time.Now()

	// Show condition: Open PR, mergeable, approved at current head by a different family
	pr := &sdk.PullRequest{
		Index:     10,
		Title:     "Feature X",
		State:     sdk.StateOpen,
		Mergeable: true,
		Poster:    &sdk.User{UserName: "agy"}, // family: google
		Head:      &sdk.PRBranchInfo{Ref: "issue/10", Sha: sha1},
		Created:   &now,
	}
	fix.prs = []*sdk.PullRequest{pr}
	fix.reviews[10] = []*sdk.PullReview{
		{
			ID:        1,
			Reviewer:  &sdk.User{UserName: "claude"}, // family: anthropic (different!)
			State:     sdk.ReviewStateApproved,
			CommitID:  sha1,
			Official:  true,
			Submitted: now,
		},
	}

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

	// 1. Show: PR is ready to merge
	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxReadyToMerge || items[0].Target != "!10" {
		t.Fatalf("expected 1 ready_to_merge item for !10, got: %+v", items)
	}

	// 2. Go-away condition 1: PR merged
	fix.mu.Lock()
	pr.HasMerged = true
	pr.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected item to go away after merge, got: %+v", items)
	}

	// Reset PR to open
	fix.mu.Lock()
	pr.HasMerged = false
	pr.State = sdk.StateOpen
	fix.mu.Unlock()

	// 3. Go-away condition 2: PR closed
	fix.mu.Lock()
	pr.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected item to go away after close, got: %+v", items)
	}

	// Reset PR to open
	fix.mu.Lock()
	pr.State = sdk.StateOpen
	fix.mu.Unlock()

	// 4. Go-away condition 3: Head moved (approval was at old SHA)
	fix.mu.Lock()
	pr.Head.Sha = "2222333344445555666677778888999900001111"
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected item to go away after head moved, got: %+v", items)
	}

	// Reset head back to sha1
	fix.mu.Lock()
	pr.Head.Sha = sha1
	fix.mu.Unlock()

	// 5. Go-away condition 4: Starts to conflict with main
	fix.mu.Lock()
	pr.Mergeable = false
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	for _, it := range items {
		if it.Kind == InboxReadyToMerge {
			t.Fatalf("expected ready_to_merge to go away when conflicting, got: %+v", items)
		}
	}

	// 6. Same family rule: Approval by actor of same family does NOT make it ready to merge
	fix.mu.Lock()
	pr.Mergeable = true
	fix.reviews[10] = []*sdk.PullReview{
		{
			ID:        2,
			Reviewer:  &sdk.User{UserName: "agy"}, // same family as author!
			State:     sdk.ReviewStateApproved,
			CommitID:  sha1,
			Official:  true,
			Submitted: now,
		},
	}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected same-family approval to be refused, got: %+v", items)
	}
}

func TestInbox_ConflictsWithMain(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	now := time.Now()
	pr := &sdk.PullRequest{
		Index:     20,
		Title:     "Feature Y",
		State:     sdk.StateOpen,
		Mergeable: false, // Conflicts!
		Poster:    &sdk.User{UserName: "claude"},
		Head:      &sdk.PRBranchInfo{Ref: "issue/20", Sha: "aaaabbbbcccc"},
		Created:   &now,
	}
	fix.prs = []*sdk.PullRequest{pr}

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

	// 1. Show: PR conflicts with main
	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxConflictsWithMain || items[0].Target != "!20" {
		t.Fatalf("expected 1 conflicts_with_main item for !20, got: %+v", items)
	}

	// 2. Go-away condition 1: Conflict resolved (becomes mergeable)
	fix.mu.Lock()
	pr.Mergeable = true
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected item to go away after conflict resolved, got: %+v", items)
	}

	// Reset to conflicting
	fix.mu.Lock()
	pr.Mergeable = false
	fix.mu.Unlock()

	// 3. Go-away condition 2: PR closed
	fix.mu.Lock()
	pr.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected item to go away after PR closed, got: %+v", items)
	}
}

func TestInbox_StoppedFlow(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	top := t.TempDir()
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
	now := time.Now()

	// Case 1: Run failed on an open issue
	issue := &sdk.Issue{
		Index:   30,
		Title:   "Issue 30",
		State:   sdk.StateOpen,
		Created: now.Add(-1 * time.Hour),
		Poster:  &sdk.User{UserName: "rlupi"},
	}
	fix.issues = []*sdk.Issue{issue}
	ended := now.Add(-25 * time.Minute)
	failedRun := secrets.Run{
		ID:      "r-fail-1",
		Issue:   30,
		Repo:    "bottown",
		Kind:    "implement",
		Actor:   "agy",
		Family:  "google",
		Created: now.Add(-30 * time.Minute),
		Ended:   &ended,
		Status:  "failed",
	}
	fix.runs = []secrets.Run{failedRun}

	// 1a. Show: Stopped flow due to failed run
	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxStoppedFlow || items[0].Target != "#30" {
		t.Fatalf("expected stopped_flow for #30 (run failed), got: %+v", items)
	}

	// 1b. Go-away: Re-run started (active run on issue)
	fix.mu.Lock()
	activeRun := secrets.Run{
		ID:      "r-live-1",
		Issue:   30,
		Repo:    "bottown",
		Kind:    "implement",
		Actor:   "agy",
		Family:  "google",
		Created: now,
		Expires: now.Add(1 * time.Hour),
	}
	fix.runs = []secrets.Run{failedRun, activeRun}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected stopped flow to go away after re-run, got: %+v", items)
	}

	// 1c. Go-away: Issue closed
	fix.mu.Lock()
	fix.runs = []secrets.Run{failedRun}
	issue.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected stopped flow to go away after issue closed, got: %+v", items)
	}

	// Case 2: Verifier undecided (review ended with COMMENT on PR current head)
	fix.mu.Lock()
	fix.issues = nil
	fix.runs = nil
	shaHead := "333344445555"
	pr := &sdk.PullRequest{
		Index:     31,
		Title:     "PR 31",
		State:     sdk.StateOpen,
		Mergeable: true,
		Poster:    &sdk.User{UserName: "agy"},
		Head:      &sdk.PRBranchInfo{Ref: "issue/31", Sha: shaHead},
		Created:   &now,
	}
	fix.prs = []*sdk.PullRequest{pr}
	fix.reviews[31] = []*sdk.PullReview{
		{
			ID:        10,
			Reviewer:  &sdk.User{UserName: "claude"},
			State:     sdk.ReviewStateComment, // undecided!
			CommitID:  shaHead,
			Submitted: now,
		},
	}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 1 || items[0].Kind != InboxStoppedFlow || items[0].Target != "!31" || !strings.Contains(items[0].Description, "COMMENT") {
		t.Fatalf("expected stopped_flow for !31 (verifier undecided), got: %+v", items)
	}

	// Case 3: Out of rounds (REQUEST_CHANGES on PR current head)
	fix.mu.Lock()
	fix.reviews[31] = []*sdk.PullReview{
		{
			ID:        11,
			Reviewer:  &sdk.User{UserName: "claude"},
			State:     sdk.ReviewStateRequestChanges, // changes requested
			CommitID:  shaHead,
			Submitted: now,
		},
	}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 1 || items[0].Kind != InboxStoppedFlow || items[0].Target != "!31" || !strings.Contains(items[0].Description, "Changes requested") {
		t.Fatalf("expected stopped_flow for !31 (out of rounds), got: %+v", items)
	}
}

func TestInbox_BrokenRun(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	top := t.TempDir()
	runsDir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	now := time.Now()

	// Subtest 1: Dead run (local status == dead)
	deadRec := runs.Record{
		ID:       "r-dead-1",
		Status:   runs.StatusDead,
		PID:      999999,
		Started:  now.Add(-10 * time.Minute),
		Worktree: filepath.Join(runsDir, "r-dead-1"),
	}
	b, _ := json.MarshalIndent(deadRec, "", "  ")
	if err := os.WriteFile(filepath.Join(runsDir, "r-dead-1.json"), b, 0o600); err != nil {
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

	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxBrokenRun || items[0].Target != "r-dead-1" {
		t.Fatalf("expected broken_run for r-dead-1, got: %+v", items)
	}
	if items[0].Action == nil || items[0].Action.Command != "bottown run gc" {
		t.Fatalf("expected dead run action 'bottown run gc', got: %+v", items[0].Action)
	}

	// Go-away condition for dead run: file removed or run cleaned up
	_ = os.Remove(filepath.Join(runsDir, "r-dead-1.json"))
	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected broken run to go away after removing dead run file, got: %+v", items)
	}

	// Subtest 2: Key expired while live (server run)
	expiredRun := secrets.Run{
		ID:      "r-expired-1",
		Repo:    "bottown",
		Kind:    "implement",
		Actor:   "agy",
		Family:  "google",
		Created: now.Add(-2 * time.Hour),
		Expires: now.Add(-1 * time.Hour), // Expired!
		Ended:   nil,                     // Key was live when expired
	}
	fix.mu.Lock()
	fix.runs = []secrets.Run{expiredRun}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 1 || items[0].Kind != InboxBrokenRun || items[0].Target != "r-expired-1" {
		t.Fatalf("expected broken_run for r-expired-1, got: %+v", items)
	}
	// The run key no longer authenticates, so `run end` cannot end it:
	// the action is gc, which revokes it with the owner key.
	if items[0].Action == nil || items[0].Action.Command != "bottown run gc" {
		t.Fatalf("expected expired run action 'bottown run gc', got: %+v", items[0].Action)
	}
	if strings.Contains(items[0].Description, "run end") {
		t.Fatalf("expired run hint still suggests run end: %q", items[0].Description)
	}

	// Go-away: Ended is set
	fix.mu.Lock()
	endedTime := now
	fix.runs[0].Ended = &endedTime
	fix.runs[0].Status = "revoked"
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected expired run to go away after Ended set, got: %+v", items)
	}

	// Subtest 3: Failed run left its worktree behind
	failedDir := filepath.Join(runsDir, "r-failed-wt")
	if err := os.MkdirAll(failedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	failedRec := runs.Record{
		ID:       "r-failed-wt",
		Status:   runs.StatusFailed,
		Started:  now.Add(-10 * time.Minute),
		Worktree: failedDir,
	}
	b2, _ := json.MarshalIndent(failedRec, "", "  ")
	if err := os.WriteFile(filepath.Join(runsDir, "r-failed-wt.json"), b2, 0o600); err != nil {
		t.Fatal(err)
	}

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 1 || items[0].Kind != InboxBrokenRun || items[0].Target != "r-failed-wt" {
		t.Fatalf("expected broken_run for failed run with worktree, got: %+v", items)
	}
	if items[0].Action == nil || items[0].Action.Command != "bottown run gc --failed" {
		t.Fatalf("expected failed run action 'bottown run gc --failed', got: %+v", items[0].Action)
	}

	// Go-away: Worktree cleaned up
	_ = os.RemoveAll(failedDir)
	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected failed run with worktree to go away after directory removed, got: %+v", items)
	}
}

func TestInbox_DiscussAnswer(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	now := time.Now()
	issue := &sdk.Issue{
		Index:   50,
		Title:   "How should X be designed?",
		State:   sdk.StateOpen,
		Created: now.Add(-2 * time.Hour),
		Poster:  &sdk.User{UserName: "rlupi"},
	}
	fix.issues = []*sdk.Issue{issue}

	// Discuss run answered on issue 50
	endedDisc := now.Add(-45 * time.Minute)
	discussRun := secrets.Run{
		ID:      "r-disc-1",
		Issue:   50,
		Repo:    "bottown",
		Kind:    "discuss",
		Actor:   "kimi",
		Family:  "moonshot",
		Created: now.Add(-1 * time.Hour),
		Ended:   &endedDisc,
		Status:  "succeeded",
	}
	fix.runs = []secrets.Run{discussRun}

	// Discuss comment by kimi on issue 50
	fix.comments[50] = []*sdk.Comment{
		{
			ID:      1,
			Poster:  &sdk.User{UserName: "kimi"},
			Body:    "Here is the answer to the question...",
			Created: now.Add(-50 * time.Minute),
		},
	}

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

	// A long thread includes the answer and subsequent owner reply
	// must still drive the inbox decision.
	filler := make([]*sdk.Comment, 50)
	for i := range filler {
		filler[i] = &sdk.Comment{Poster: &sdk.User{UserName: "claude"}, Created: now.Add(-2 * time.Hour)}
	}
	fix.comments[50] = append(filler, fix.comments[50]...)

	// 1. Show: Discuss answer waiting on owner reply
	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxDiscussAnswer || items[0].Target != "#50" {
		t.Fatalf("expected discuss_answer for #50, got: %+v", items)
	}

	// 2. Go-away condition 1: Owner replies
	fix.mu.Lock()
	fix.comments[50] = append(fix.comments[50], &sdk.Comment{
		ID:      2,
		Poster:  &sdk.User{UserName: "rlupi"}, // owner commented!
		Body:    "Thanks, looks good.",
		Created: now.Add(-10 * time.Minute),
	})
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected discuss answer to go away after owner replies, got: %+v", items)
	}

	// A later comment by the same actor (e.g. an implement handover) is
	// not the answer: the owner's reply still counts.
	fix.mu.Lock()
	fix.comments[50] = append(fix.comments[50], &sdk.Comment{
		ID:      3,
		Poster:  &sdk.User{UserName: "kimi"},
		Body:    "bottown implement run handover",
		Created: now.Add(-5 * time.Minute),
	})
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected a later discussant comment not to revive the answer, got: %+v", items)
	}

	// 3. Go-away condition 2: Issue closed
	fix.mu.Lock()
	fix.comments[50] = fix.comments[50][:51] // remove owner reply
	issue.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected discuss answer to go away after issue closed, got: %+v", items)
	}

	// 4. Non-discuss run comments must NOT trigger discuss answer
	fix.mu.Lock()
	issue.State = sdk.StateOpen
	fix.runs[0].Kind = "implement" // not a discuss run!
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected actor comment without discuss run to not trigger discuss_answer, got: %+v", items)
	}
}

func TestInbox_ActorFiledIssue(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	now := time.Now()
	// Filed by actor claude, no milestone, no queue labels
	issue := &sdk.Issue{
		Index:     60,
		Title:     "Found a bug in X",
		State:     sdk.StateOpen,
		Created:   now.Add(-30 * time.Minute),
		Poster:    &sdk.User{UserName: "claude"}, // actor!
		Milestone: nil,
		Labels:    []*sdk.Label{},
	}
	fix.issues = []*sdk.Issue{issue}

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

	// 1. Show: Actor filed issue not triaged
	items, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != InboxActorFiledIssue || items[0].Target != "#60" {
		t.Fatalf("expected actor_filed_issue for #60, got: %+v", items)
	}

	// 2. Go-away condition 1: Milestone assigned
	fix.mu.Lock()
	issue.Milestone = &sdk.Milestone{ID: 1, Title: "Sprint 1"}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected actor filed issue to go away when milestone assigned, got: %+v", items)
	}

	// Reset milestone
	fix.mu.Lock()
	issue.Milestone = nil
	fix.mu.Unlock()

	// 3. Go-away condition 2: Queued (requires both implement/<actor> AND verify/<actor> labels per §3.6)
	fix.mu.Lock()
	issue.Labels = []*sdk.Label{{Name: "implement/agy"}}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 1 {
		t.Fatalf("expected actor filed issue to remain in inbox when only implement label is set, got: %+v", items)
	}

	fix.mu.Lock()
	issue.Labels = []*sdk.Label{{Name: "implement/agy"}, {Name: "verify/claude"}}
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected actor filed issue to go away when fully queued (both implement and verify), got: %+v", items)
	}

	// Reset label
	fix.mu.Lock()
	issue.Labels = nil
	fix.mu.Unlock()

	// 4. Go-away condition 3: Issue closed
	fix.mu.Lock()
	issue.State = sdk.StateClosed
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected actor filed issue to go away when closed, got: %+v", items)
	}

	// 5. Non-actor filed issue (owner filed) should NOT appear
	fix.mu.Lock()
	issue.State = sdk.StateOpen
	issue.Poster = &sdk.User{UserName: "rlupi"} // human owner
	fix.mu.Unlock()

	items, _ = s.ComputeInbox(ctx)
	if len(items) != 0 {
		t.Fatalf("expected human-filed issue to not be considered actor_filed_issue, got: %+v", items)
	}
}

func TestInbox_FetchError(t *testing.T) {
	// A server that returns 500 Internal Server Error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server unreachable", http.StatusInternalServerError)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port

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

	// ComputeInbox directly must return an error
	items, err := s.ComputeInbox(ctx)
	if err == nil {
		t.Fatalf("expected error from ComputeInbox when server returns 500, got items: %+v", items)
	}

	// ComputeStatus must catch the error, populate InboxError, and leave Inbox empty
	status, err := s.ComputeStatus(ctx, false)
	if err != nil {
		t.Fatalf("ComputeStatus should not return top-level error: %v", err)
	}
	if status.InboxError == "" {
		t.Fatalf("expected status.InboxError to be set, got empty")
	}
	if len(status.Inbox) != 0 {
		t.Fatalf("expected status.Inbox to be empty on error, got: %+v", status.Inbox)
	}
}

func TestInbox_PagesAllOpenPRsAndIssues(t *testing.T) {
	fix := defaultFixture()
	_, port := newFixtureServer(t, fix)

	now := time.Now()
	sha := "1111222233334444555566667777888899990000"

	// 60 filler PRs that produce no inbox items, then a ready-to-merge PR
	// that would fall off a single limit=50 page.
	for i := 1; i <= 60; i++ {
		fix.prs = append(fix.prs, &sdk.PullRequest{
			Index:     int64(i),
			State:     sdk.StateOpen,
			Mergeable: true,
			Poster:    &sdk.User{UserName: "claude"},
			Head:      &sdk.PRBranchInfo{Ref: "issue/filler", Sha: sha},
			Created:   &now,
		})
	}
	fix.prs = append(fix.prs, &sdk.PullRequest{
		Index:     61,
		State:     sdk.StateOpen,
		Mergeable: true,
		Poster:    &sdk.User{UserName: "agy"}, // google
		Head:      &sdk.PRBranchInfo{Ref: "issue/61", Sha: sha},
		Created:   &now,
	})
	fix.reviews[61] = []*sdk.PullReview{{
		ID:        1,
		Reviewer:  &sdk.User{UserName: "claude"}, // anthropic, cross-family
		State:     sdk.ReviewStateApproved,
		CommitID:  sha,
		Official:  true,
		Submitted: now,
	}}

	// The decisive approval is on a later review page.
	for i := 0; i < 50; i++ {
		fix.reviews[61] = append([]*sdk.PullReview{{State: sdk.ReviewStateComment}}, fix.reviews[61]...)
	}

	// 60 filler issues by the owner (not actor-filed), then an actor-filed
	// issue beyond the first limit=50 page.
	for i := 1; i <= 60; i++ {
		fix.issues = append(fix.issues, &sdk.Issue{
			Index:   int64(i),
			State:   sdk.StateOpen,
			Title:   "filler",
			Poster:  &sdk.User{UserName: "rlupi"},
			Created: now,
		})
	}
	fix.issues = append(fix.issues, &sdk.Issue{
		Index:   61,
		State:   sdk.StateOpen,
		Title:   "actor-filed beyond page 1",
		Poster:  &sdk.User{UserName: "claude"},
		Created: now,
	})

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

	items, err := s.ComputeInbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var ready, filed bool
	for _, it := range items {
		if it.Kind == InboxReadyToMerge && it.Target == "!61" {
			ready = true
		}
		if it.Kind == InboxActorFiledIssue && it.Target == "#61" {
			filed = true
		}
	}
	if !ready {
		t.Fatalf("ready-to-merge PR on page 2 missing; items: %+v", items)
	}
	if !filed {
		t.Fatalf("actor-filed issue on page 2 missing; items: %+v", items)
	}
}
