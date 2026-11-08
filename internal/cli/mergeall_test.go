package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// fakePR is one pull request in the merge-all tests' fake server.
type fakePR struct {
	number     int64
	title      string
	author     string
	headBranch string
	headSHA    string
	baseBranch string
	decision   review.Decision
	reviewer   string
	draft      bool
	merged     bool
}

// fakeForge is a fake bottown server: the reads merge-all makes, and the
// landing, check-status and merge writes it causes. Tests mutate its state
// between landings (a sibling merging, a conflict appearing).
type fakeForge struct {
	t *testing.T

	mu        sync.Mutex
	prs       map[int64]*fakePR
	order     []int64
	issues    map[int64]server.IssueOut
	live      []secrets.Run
	conflicts map[int64][]string
	files     map[int64][]string
	lost      map[int64]bool

	landings []string
	merges   []string
	statuses []string
	srv      *httptest.Server
}

func newFakeForge(t *testing.T) *fakeForge {
	f := &fakeForge{t: t, prs: map[int64]*fakePR{}, issues: map[int64]server.IssueOut{},
		conflicts: map[int64][]string{}, files: map[int64][]string{}, lost: map[int64]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForge) add(p *fakePR) *fakePR {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.decision == "" {
		p.decision = review.Approved
	}
	f.prs[p.number] = p
	f.order = append(f.order, p.number)
	return p
}

func (f *fakeForge) client() *client.Client { return client.New(f.srv.URL, "k") }

func (f *fakeForge) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/forge/prs":
		var out []server.PRItem
		for _, n := range f.order {
			p := f.prs[n]
			if p.merged {
				continue
			}
			out = append(out, f.prItem(p))
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/v1/forge/pr":
		p := f.byNumber(r.URL.Query().Get("number"))
		if p == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.prOut(p))
	case "/v1/forge/pr/merge-base":
		p := f.byNumber(r.URL.Query().Get("number"))
		if p == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(server.PRMergeStatus{
			HeadSHA: p.headSHA, Clean: len(f.conflicts[p.number]) == 0,
			Conflicts: f.conflicts[p.number], Files: f.files[p.number],
		})
	case "/v1/forge/pr/landing":
		var in server.PRLandingIn
		_ = json.NewDecoder(r.Body).Decode(&in)
		p := f.prs[in.Number]
		f.landings = append(f.landings, fmt.Sprintf("!%d@%s", in.Number, in.HeadSHA))
		p.headSHA = fmt.Sprintf("land-%d-%d", in.Number, len(f.landings))
		decision := review.Approved
		if f.lost[in.Number] {
			decision = review.NeedsReview
		}
		p.decision = decision
		_ = json.NewEncoder(w).Encode(server.PRLandingOut{HeadSHA: p.headSHA, BaseSHA: "base", Assessment: review.Assessment{Decision: decision}})
	case "/v1/forge/pr/merge":
		var in server.LandIn
		_ = json.NewDecoder(r.Body).Decode(&in)
		p := f.prs[in.Number]
		f.merges = append(f.merges, fmt.Sprintf("!%d@%s", in.Number, in.HeadSHA))
		p.merged = true
		_ = json.NewEncoder(w).Encode(server.LandOut{Number: in.Number, HeadSHA: in.HeadSHA, MergeCommit: in.HeadSHA})
	case "/v1/forge/status":
		var in server.CommitStatusIn
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.statuses = append(f.statuses, fmt.Sprintf("%s@%s:%s", in.Check, in.SHA, in.State))
	case "/v1/forge/issue":
		n := int64(atoiOrZero(r.URL.Query().Get("number")))
		is, ok := f.issues[n]
		if !ok {
			is = server.IssueOut{Number: n, Author: "owner"}
		}
		_ = json.NewEncoder(w).Encode(is)
	case "/v1/runs":
		_ = json.NewEncoder(w).Encode(f.live)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *fakeForge) byNumber(s string) *fakePR { return f.prs[int64(atoiOrZero(s))] }

func atoiOrZero(s string) int {
	var n int64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return int(n)
}

func (f *fakeForge) prItem(p *fakePR) server.PRItem {
	a := f.assessment(p)
	return server.PRItem{Number: p.number, Title: p.title, State: "open", Merged: p.merged, Author: p.author,
		HeadBranch: p.headBranch, HeadSHA: p.headSHA, BaseBranch: p.baseBranch, Draft: p.draft, Assessment: &a}
}

func (f *fakeForge) prOut(p *fakePR) server.PROut {
	a := f.assessment(p)
	return server.PROut{Number: p.number, Title: p.title, State: "open", Merged: p.merged, Author: p.author,
		HeadBranch: p.headBranch, HeadSHA: p.headSHA, BaseBranch: p.baseBranch, Assessment: a}
}

func (f *fakeForge) assessment(p *fakePR) review.Assessment {
	a := review.Assessment{Decision: p.decision}
	if p.reviewer != "" {
		a.Review = &review.Review{Reviewer: p.reviewer, State: review.StateApproved, CommitID: p.headSHA, Official: true}
	}
	return a
}

// okChecker is a checker that passes or fails every group without running
// anything.
type okChecker struct {
	passes   bool
	requests []flow.CheckRequest
}

func (c *okChecker) LogPath(flow.CheckRequest) string { return "checks.log" }
func (c *okChecker) Check(_ context.Context, r flow.CheckRequest) (flow.CheckOutcome, error) {
	c.requests = append(c.requests, r)
	o := flow.CheckOutcome{Passed: c.passes, Description: "checked", Log: "checks.log"}
	for _, g := range r.Groups {
		o.Groups = append(o.Groups, flow.GroupOutcome{Group: g, Passed: c.passes})
	}
	return o, nil
}

// newMergeAlly builds a mergeAll over the fake server.
func newMergeAlly(t *testing.T, f *fakeForge, out *bytes.Buffer, actor, verify string) *mergeAll {
	return &mergeAll{
		out: out, repo: "demo", owner: "owner", base: "main", api: f.client(),
		ws:    &runs.Workspace{RunsDir: t.TempDir()},
		actor: actor, verify: verify, checker: &okChecker{passes: true},
		timeout: time.Hour, checkTimeout: time.Minute,
	}
}

func TestMergeAllDefaultBatchSkipsMilestones(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/10", headSHA: "h1", baseBranch: "main"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "milestone/demo", headSHA: "h2", baseBranch: "main"})
	f.add(&fakePR{number: 3, author: "amy", headBranch: "issue/11", headSHA: "h3", baseBranch: "main"})
	f.add(&fakePR{number: 4, author: "amy", headBranch: "issue/12", headSHA: "h4", baseBranch: "main", draft: true})
	f.issues[10] = server.IssueOut{Number: 10, Author: "owner", Body: "Work.\n\n" + milestoneChildMarker()}
	f.issues[11] = server.IssueOut{Number: 11, Author: "owner", Body: "Work."}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "", "")
	if _, err := m.run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.prs[3].merged != true {
		t.Errorf("the plain PR did not land:\n%s", out.String())
	}
	for _, n := range []int64{1, 2, 4} {
		if f.prs[n].merged {
			t.Errorf("!%d landed in the default batch:\n%s", n, out.String())
		}
	}

	// Named, the milestone child and the milestone/* PR land.
	out.Reset()
	f2 := newFakeForge(t)
	f2.add(&fakePR{number: 1, author: "amy", headBranch: "issue/10", headSHA: "h1", baseBranch: "main"})
	f2.add(&fakePR{number: 2, author: "amy", headBranch: "milestone/demo", headSHA: "h2", baseBranch: "main"})
	f2.issues[10] = server.IssueOut{Number: 10, Author: "owner", Body: "Work.\n\n" + milestoneChildMarker()}
	m2 := newMergeAlly(t, f2, &out, "", "")
	if _, err := m2.run(t.Context(), []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if !f2.prs[1].merged || !f2.prs[2].merged {
		t.Errorf("named milestone PRs did not land:\n%s", out.String())
	}
}

func milestoneChildMarker() string {
	return `<!-- bottown:milestone v=1 plan="demo" key="a" sha="0123456789abcdef" -->`
}

func TestMergeAllOrderFollowed(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/1", headSHA: "h1", baseBranch: "main"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "issue/2", headSHA: "h2", baseBranch: "main"})
	f.add(&fakePR{number: 3, author: "amy", headBranch: "issue/3", headSHA: "h3", baseBranch: "main"})
	// #1 and #2 share a.go; #3 touches b.go alone, so it lands first. #1
	// and #2 then tie on overlap, so the lower number goes first.
	f.files[1] = []string{"a.go"}
	f.files[2] = []string{"a.go"}
	f.files[3] = []string{"b.go"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "", "")
	if _, err := m.run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"3", "1", "2"}
	if got := mergeOrder(f.merges); !slices.Equal(got, want) {
		t.Errorf("merges = %v, want order %v\n%s", f.merges, want, out.String())
	}
}

// mergeOrder is the PR numbers of the recorded merges, in order.
func mergeOrder(merges []string) []string {
	var out []string
	for _, m := range merges {
		n, _, _ := strings.Cut(strings.TrimPrefix(m, "!"), "@")
		out = append(out, n)
	}
	return out
}

func TestMergeAllConflictReportedAndContinues(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/1", headSHA: "h1", baseBranch: "main"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "issue/2", headSHA: "h2", baseBranch: "main"})
	f.add(&fakePR{number: 3, author: "amy", headBranch: "issue/3", headSHA: "h3", baseBranch: "main"})
	f.conflicts[1] = []string{"a.go"}
	f.files[1] = []string{"a.go"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "", "")
	ok, err := m.run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("run reported every candidate landed")
	}
	if !f.prs[2].merged || !f.prs[3].merged {
		t.Errorf("the clean candidates did not land:\n%s", out.String())
	}
	if f.prs[1].merged {
		t.Error("the conflicting PR landed")
	}
	if !strings.Contains(out.String(), "conflict: a.go") {
		t.Errorf("the conflict was not reported:\n%s", out.String())
	}
}

func TestMergeAllRepairPairAndReland(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/10", headSHA: "h1", baseBranch: "main"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "issue/12", headSHA: "h2", baseBranch: "main"})
	f.conflicts[1] = []string{"a.go"}
	f.issues[10] = server.IssueOut{Number: 10, Author: "owner", Labels: []string{"implement/pat", "verify/vera"}}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "nick", "")
	var got struct {
		issue             int64
		implement, verify string
	}
	m.runFlow = func(_ context.Context, issue int64, implement, verify string) error {
		got.issue, got.implement, got.verify = issue, implement, verify
		if !f.prs[2].merged {
			t.Errorf("the repair ran before the clean candidate landed:\n%s", out.String())
		}
		f.mu.Lock()
		delete(f.conflicts, 1)
		f.prs[1].headSHA = "repaired"
		f.mu.Unlock()
		return nil
	}
	ok, err := m.run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.issue != 10 || got.implement != "nick" || got.verify != "vera" {
		t.Errorf("repair pair = %+v, want issue 10, nick, vera", got)
	}
	if !ok || !f.prs[1].merged {
		t.Errorf("the repaired PR did not land:\n%s", out.String())
	}
	if !strings.Contains(strings.Join(f.landings, ","), "!1@repaired") {
		t.Errorf("the landing was not built on the repaired head: %v", f.landings)
	}
}

func TestMergeAllRepairFallsBackToAuthorAndApprover(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/10", headSHA: "h1", baseBranch: "main", reviewer: "vera"})
	f.conflicts[1] = []string{"a.go"}
	f.issues[10] = server.IssueOut{Number: 10, Author: "owner"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "nick", "")
	var implement, verify string
	m.runFlow = func(_ context.Context, _ int64, i, v string) error {
		implement, verify = i, v
		f.mu.Lock()
		delete(f.conflicts, 1)
		f.mu.Unlock()
		return nil
	}
	if _, err := m.run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if implement != "nick" || verify != "vera" {
		t.Errorf("pair = %s, %s; want nick, vera", implement, verify)
	}
}

func TestMergeAllRepairOwnerApprovalReported(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/10", headSHA: "h1", baseBranch: "main"})
	f.conflicts[1] = []string{"a.go"}
	f.issues[10] = server.IssueOut{Number: 10, Author: "owner"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "nick", "")
	called := false
	m.runFlow = func(context.Context, int64, string, string) error { called = true; return nil }
	if _, err := m.run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("the repair ran for an owner-approved PR")
	}
	if !strings.Contains(out.String(), "no verifier on record") {
		t.Errorf("the owner approval was not reported:\n%s", out.String())
	}
}

func TestMergeAllFinalMilestonePRNotRepaired(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 5, author: "amy", headBranch: "milestone/demo", headSHA: "h5", baseBranch: "main"})
	f.conflicts[5] = []string{"a.go"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "nick", "")
	called := false
	m.runFlow = func(context.Context, int64, string, string) error { called = true; return nil }
	if _, err := m.run(t.Context(), []int64{5}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("the final milestone PR was repaired")
	}
	if !strings.Contains(out.String(), "not repaired") {
		t.Errorf("the final milestone PR was not reported:\n%s", out.String())
	}
}

func TestMergeAllDryRunTouchesNothing(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/1", headSHA: "h1", baseBranch: "main"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "issue/2", headSHA: "h2", baseBranch: "main"})
	f.files[1] = []string{"a.go"}
	f.files[2] = []string{"a.go"}

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "", "")
	m.dryRun = true
	if _, err := m.run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.landings) != 0 || len(f.merges) != 0 || len(f.statuses) != 0 {
		t.Errorf("dry run wrote: landings %v, merges %v, statuses %v", f.landings, f.merges, f.statuses)
	}
	if !strings.Contains(out.String(), "overlaps 1 other candidate") {
		t.Errorf("dry run did not report the overlap:\n%s", out.String())
	}
}

func TestMergeAllChecksFailReported(t *testing.T) {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/1", headSHA: "h1", baseBranch: "main"})

	var out bytes.Buffer
	m := newMergeAlly(t, f, &out, "", "")
	m.checker = &okChecker{passes: false}
	ok, err := m.run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("run reported success after failed checks")
	}
	if len(f.merges) != 0 {
		t.Error("the PR merged after failed checks")
	}
	if !strings.Contains(out.String(), "checks failed") {
		t.Errorf("the failure was not reported:\n%s", out.String())
	}
}

func TestSyncMainCheckout(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git(t, dir, "init", "-q", "--bare", remote)
	work := filepath.Join(dir, "work")
	git(t, dir, "clone", "-q", remote, work)
	git(t, work, "config", "user.email", "a@b.c")
	git(t, work, "config", "user.name", "a")
	git(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, work, "branch", "-M", "main")
	git(t, work, "push", "-q", "-u", "origin", "main")
	git(t, work, "remote", "set-head", "origin", "-a")

	other := filepath.Join(dir, "other")
	git(t, dir, "clone", "-q", remote, other)
	git(t, other, "config", "user.email", "a@b.c")
	git(t, other, "config", "user.name", "a")
	git(t, other, "commit", "-q", "--allow-empty", "-m", "two")
	git(t, other, "push", "-q", "origin", "main")

	var out bytes.Buffer
	base, err := syncMainCheckout(t.Context(), &out, work, "origin", false)
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" {
		t.Errorf("base = %q, want main", base)
	}
	if head := git(t, work, "rev-parse", "HEAD"); head != git(t, other, "rev-parse", "HEAD") {
		t.Errorf("main did not fast-forward to the remote tip")
	}

	// Diverged: a local commit the remote does not have.
	git(t, work, "commit", "-q", "--allow-empty", "-m", "local")
	git(t, other, "commit", "-q", "--allow-empty", "-m", "three")
	git(t, other, "push", "-q", "origin", "main")
	if _, err := syncMainCheckout(t.Context(), &out, work, "origin", false); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Errorf("diverged main: err = %v, want a divergence stop", err)
	}
}

func TestSyncMainCheckoutRefusesDirty(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git(t, dir, "init", "-q", "--bare", remote)
	work := filepath.Join(dir, "work")
	git(t, dir, "clone", "-q", remote, work)
	git(t, work, "config", "user.email", "a@b.c")
	git(t, work, "config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(work, "tracked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "tracked")
	git(t, work, "commit", "-q", "-m", "one")
	git(t, work, "branch", "-M", "main")
	git(t, work, "push", "-q", "-u", "origin", "main")
	git(t, work, "remote", "set-head", "origin", "-a")

	other := filepath.Join(dir, "other")
	git(t, dir, "clone", "-q", remote, other)
	git(t, other, "config", "user.email", "a@b.c")
	git(t, other, "config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(other, "tracked"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, other, "commit", "-q", "-am", "two")
	git(t, other, "push", "-q", "origin", "main")

	// A tracked local edit and a remote tip to fast-forward to.
	if err := os.WriteFile(filepath.Join(work, "tracked"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := syncMainCheckout(t.Context(), &out, work, "origin", false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("dirty main: err = %v, want an uncommitted-changes stop", err)
	}
}

// git runs git in dir and returns trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
