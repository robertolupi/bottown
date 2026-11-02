package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/secrets"
)

// milestoneFixture is the forge and server state GET /api/milestones reads.
type milestoneFixture struct {
	actors     []map[string]any
	issues     []*sdk.Issue
	milestones []*sdk.Milestone
	prs        []*sdk.PullRequest
	reviews    map[int64][]*sdk.PullReview
	comments   map[int64][]*sdk.Comment
	deps       map[int64][]int64
	branches   []string
	runs       []secrets.Run
}

func (f *milestoneFixture) serve(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	byNum := map[int64]*sdk.Issue{}
	for _, is := range f.issues {
		byNum[is.Index] = is
	}
	num := func(parts []string, i int) int64 {
		n, _ := strconv.ParseInt(parts[len(parts)+i], 10, 64)
		return n
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		parts := strings.Split(p, "/")
		var out any
		switch {
		case p == "/v1/actors":
			out = f.actors
		case p == "/v1/runs":
			out = f.runs
		case strings.Contains(p, "/branches/"):
			name := p[strings.Index(p, "/branches/")+len("/branches/"):]
			if !slices.Contains(f.branches, name) {
				http.NotFound(w, r)
				return
			}
			out = sdk.Branch{Name: name}
		case strings.HasSuffix(p, "/dependencies"):
			list := []*sdk.Issue{}
			for _, d := range f.deps[num(parts, -2)] {
				list = append(list, byNum[d])
			}
			out = paginate(r, list)
		case strings.HasSuffix(p, "/comments"):
			out = f.comments[num(parts, -2)]
		case strings.HasSuffix(p, "/reviews"):
			out = paginate(r, f.reviews[num(parts, -2)])
		case strings.HasSuffix(p, "/issues"):
			out = paginate(r, f.issues)
		case strings.HasSuffix(p, "/milestones"):
			out = paginate(r, f.milestones)
		case strings.HasSuffix(p, "/pulls"):
			out = paginate(r, f.prs)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return ln.Addr().(*net.TCPAddr).Port
}

const epicHeader = "_Implementation Epic for kickoff #1, filed by `bottown milestone apply` from [`docs/milestones/demo.md`](/bottown/bottown/src/commit/abc/docs/milestones/demo.md) at `abc`. Design: [`docs/d.md`](/bottown/bottown/src/branch/main/docs/d.md)._\n\n## Implementation issues\n"

func childBody(plan, key string) string {
	return "Body.\n\n<!-- bottown:milestone v=1 plan=\"" + plan + "\" key=\"" + key + "\" sha=\"0123456789abcdef\" -->"
}

func TestComputeMilestones(t *testing.T) {
	now := time.Now()
	owner := &sdk.User{UserName: "rlupi"}
	agy := &sdk.User{UserName: "agy"}
	ms7 := &sdk.Milestone{ID: 7, Title: "Demo", State: sdk.StateOpen}
	ms8 := &sdk.Milestone{ID: 8, Title: "Loose", State: sdk.StateOpen}
	ms9 := &sdk.Milestone{ID: 9, Title: "Done", State: sdk.StateClosed}
	issue := func(n int64, title string, poster *sdk.User, body string, ms *sdk.Milestone, state sdk.StateType) *sdk.Issue {
		return &sdk.Issue{Index: n, Title: title, Poster: poster, Body: body, Milestone: ms, State: state}
	}
	stopped := issue(27, "Golf", owner, childBody("demo", "g"), ms7, sdk.StateOpen)
	stopped.Labels = []*sdk.Label{{Name: "implement/kimi"}, {Name: "verify/claude"}}
	f := &milestoneFixture{
		actors: []map[string]any{
			{"name": "claude", "family": "anthropic", "harness": "claude"},
			{"name": "agy", "family": "google", "harness": "agy"},
			{"name": "kimi", "family": "moonshot", "harness": "kimi"},
		},
		issues: []*sdk.Issue{
			issue(1, "Kickoff", owner, "Design it.", ms7, sdk.StateOpen),
			issue(5, "External", owner, "Elsewhere.", nil, sdk.StateClosed),
			issue(20, "Demo epic", owner, epicHeader+"\n<!-- bottown:milestone v=1 plan=\"demo\" role=\"epic\" sha=\"0123456789abcdef\" -->", ms7, sdk.StateOpen),
			issue(21, "Alpha", owner, childBody("demo", "a"), ms7, sdk.StateClosed),
			issue(22, "Bravo", owner, childBody("demo", "b"), ms7, sdk.StateOpen),
			issue(23, "Charlie", owner, childBody("demo", "c"), ms7, sdk.StateOpen),
			issue(24, "Delta", owner, childBody("demo", "d"), ms7, sdk.StateOpen),
			issue(25, "Echo", owner, childBody("demo", "e"), ms7, sdk.StateOpen),
			issue(26, "Foxtrot", owner, childBody("demo", "f"), ms7, sdk.StateOpen),
			stopped,
			issue(28, "Hotel", owner, childBody("demo", "h"), ms7, sdk.StateOpen),
			// An actor's copies of the markers count for nothing, even
			// on an issue the epic is blocked by.
			issue(29, "Spoofed child", agy, childBody("demo", "x"), ms7, sdk.StateOpen),
			issue(30, "Spoofed epic", agy, epicHeader+"\n<!-- bottown:milestone v=1 plan=\"evil\" role=\"epic\" sha=\"0123456789abcdef\" -->", ms8, sdk.StateOpen),
			issue(40, "Loose issue", owner, "No plan.", ms8, sdk.StateOpen),
			issue(41, "Old issue", owner, "Done.", ms9, sdk.StateClosed),
		},
		milestones: []*sdk.Milestone{ms7, ms8, ms9},
		deps: map[int64][]int64{
			20: {21, 22, 23, 24, 25, 26, 27, 29},
			22: {21, 5},
			23: {22},
			24: {21},
		},
		branches: []string{"milestone/20"},
		prs: []*sdk.PullRequest{
			{Index: 50, Title: "Echo PR", State: sdk.StateOpen, Poster: agy, Head: &sdk.PRBranchInfo{Ref: "issue/25", Sha: "e1"}},
			{Index: 51, Title: "Foxtrot PR", State: sdk.StateOpen, Poster: agy, Head: &sdk.PRBranchInfo{Ref: "issue/26", Sha: "f1"}},
			{Index: 49, Title: "Alpha PR", State: sdk.StateClosed, HasMerged: true, Poster: agy, Head: &sdk.PRBranchInfo{Ref: "issue/21", Sha: "a1"}},
		},
		reviews: map[int64][]*sdk.PullReview{
			50: {{ID: 1, Reviewer: &sdk.User{UserName: "claude"}, State: sdk.ReviewStateApproved, CommitID: "e1", Official: true, Submitted: now}},
		},
		comments: map[int64][]*sdk.Comment{
			27: {
				{Poster: owner, Body: "**bottown flow: out of rounds**\n\nThree requests.", Created: now.Add(-time.Minute)},
				// Only the owner's flow posts count.
				{Poster: agy, Body: "**bottown flow: ready to merge**", Created: now},
			},
		},
		runs: []secrets.Run{
			{ID: "r-live", Kind: "implement", Actor: "agy", Repo: "bottown", Issue: 24, Created: now.Add(-2 * time.Minute), Expires: now.Add(time.Hour)},
			{ID: "r-old", Kind: "implement", Actor: "kimi", Repo: "bottown", Issue: 27, Created: now.Add(-time.Hour), Expires: now, Ended: &now, Status: "succeeded"},
			{ID: "r-e", Kind: "implement", Actor: "agy", Repo: "bottown", Issue: 25, Created: now.Add(-time.Hour), Ended: &now, Status: "succeeded"},
			{ID: "r-e2", Kind: "verify", Actor: "claude", Repo: "bottown", PR: 50, Created: now.Add(-30 * time.Minute), Ended: &now, Status: "succeeded"},
		},
	}
	port := f.serve(t)
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	groups, err := s.ComputeMilestones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	if !slices.Equal(ids, []string{"epic-20", "milestone-8"}) {
		t.Fatalf("groups %v", ids)
	}

	g := groups[0]
	if g.Error != "" {
		t.Fatalf("error: %s", g.Error)
	}
	if g.Plan != "demo" || g.Epic.Number != 20 || g.Milestone.ID != 7 || g.Kickoff.Number != 1 || g.Kickoff.Title != "Kickoff" ||
		g.Design != "docs/d.md" || g.PlanFile != "docs/milestones/demo.md" || g.Base != "milestone/20" {
		t.Errorf("summary: %+v", g)
	}
	if len(g.Removed) != 1 || g.Removed[0].Number != 28 || g.Removed[0].Key != "h" {
		t.Errorf("removed: %+v", g.Removed)
	}
	want := MilestoneCounts{Total: 7, Closed: 1, Ready: 1, Running: 1, InReview: 1, AwaitingMerge: 1, Blocked: 1, Stopped: 1}
	if g.Counts != want {
		t.Errorf("counts %+v, want %+v", g.Counts, want)
	}

	nodes := map[int64]MilestoneNode{}
	for _, n := range g.Nodes {
		nodes[n.Number] = n
	}
	if _, ok := nodes[29]; ok {
		t.Error("the actor-authored child is a node")
	}
	for n, w := range map[int64]struct {
		status string
		layer  int
	}{
		5: {"closed", 0}, 21: {NodeClosed, 0}, 22: {NodeReady, 1}, 23: {NodeBlocked, 2}, 24: {NodeRunning, 1},
		25: {NodeApproved, 0}, 26: {NodeInReview, 0}, 27: {NodeStopped, 0},
	} {
		if got := nodes[n]; got.Status != w.status || got.Layer != w.layer {
			t.Errorf("#%d: status %q layer %d, want %q %d", n, got.Status, got.Layer, w.status, w.layer)
		}
	}
	if !nodes[5].External || nodes[22].External || nodes[22].Key != "b" {
		t.Errorf("external/key: %+v %+v", nodes[5], nodes[22])
	}
	if g.Layers != 3 {
		t.Errorf("layers %d", g.Layers)
	}
	edges := g.Edges
	slices.SortFunc(edges, func(a, b MilestoneEdge) int { return int(a.To*100 + a.From - b.To*100 - b.From) })
	if !slices.Equal(edges, []MilestoneEdge{{From: 5, To: 22}, {From: 21, To: 22}, {From: 22, To: 23}, {From: 21, To: 24}}) {
		t.Errorf("edges %v", edges)
	}
	if r := nodes[24].Run; r == nil || r.ID != "r-live" || r.Actor != "agy" || nodes[24].Implement != "agy" {
		t.Errorf("running node: %+v", nodes[24])
	}
	if n := nodes[25]; n.PR == nil || n.PR.Number != 50 || n.PR.Review != "approved" || n.Implement != "agy" || n.Verify != "claude" {
		t.Errorf("approved node: %+v %+v", n, n.PR)
	}
	if n := nodes[27]; n.StopReason != "out of rounds" || n.Implement != "kimi" || n.Verify != "claude" {
		t.Errorf("stopped node: %+v", n)
	}
	if n := nodes[21]; n.PR == nil || n.PR.Number != 49 || n.PR.State != "merged" {
		t.Errorf("closed node's PR: %+v", n.PR)
	}

	loose := groups[1]
	if loose.Epic != nil || loose.Base != "main" || len(loose.Nodes) != 2 || loose.Counts.Total != 2 || loose.Counts.Ready != 2 {
		t.Errorf("milestone without epic: %+v", loose)
	}
}

func TestComputeMilestonesStopReasonSuperseded(t *testing.T) {
	now := time.Now()
	owner := &sdk.User{UserName: "rlupi"}
	ms := &sdk.Milestone{ID: 3, Title: "M", State: sdk.StateOpen}
	f := &milestoneFixture{
		issues: []*sdk.Issue{
			{Index: 2, Title: "Epic", Poster: owner, Milestone: ms, State: sdk.StateOpen, Body: epicHeader + "\n<!-- bottown:milestone v=1 plan=\"p\" role=\"epic\" sha=\"0123456789abcdef\" -->"},
			{Index: 3, Title: "Child", Poster: owner, Milestone: ms, State: sdk.StateOpen, Body: childBody("p", "c")},
			{Index: 4, Title: "Failed", Poster: owner, Milestone: ms, State: sdk.StateOpen, Body: childBody("p", "f")},
		},
		milestones: []*sdk.Milestone{ms},
		deps:       map[int64][]int64{2: {3, 4}},
		comments: map[int64][]*sdk.Comment{
			3: {{Poster: owner, Body: "**bottown flow: run failed**\n\nboom", Created: now.Add(-time.Hour)}},
		},
		runs: []secrets.Run{
			// A run after the stop: the owner re-ran the issue.
			{ID: "r-1", Kind: "implement", Actor: "agy", Issue: 3, Created: now.Add(-time.Minute), Ended: &now, Status: "succeeded"},
			{ID: "r-2", Kind: "implement", Actor: "agy", Issue: 4, Created: now.Add(-time.Minute), Ended: &now, Status: "failed"},
		},
	}
	port := f.serve(t)
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	groups, err := s.ComputeMilestones(context.Background())
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups %v, %v", groups, err)
	}
	for _, n := range groups[0].Nodes {
		switch n.Number {
		case 3:
			if n.Status != NodeReady {
				t.Errorf("#3 %q (%s), want ready", n.Status, n.StopReason)
			}
		case 4:
			if n.Status != NodeStopped || n.StopReason != "run failed" {
				t.Errorf("#4 %q (%s), want stopped: run failed", n.Status, n.StopReason)
			}
		}
	}
}

func TestLayerize(t *testing.T) {
	got := layerize([]int64{1, 2, 3, 4, 5, 6}, map[int64][]int64{
		2: {1}, 3: {1, 2}, 4: {99}, // 99 is outside the graph
		5: {6}, 6: {5}, // a cycle
	})
	want := map[int64]int{1: 0, 2: 1, 3: 2, 4: 0, 5: 3, 6: 3}
	for n, l := range want {
		if got[n] != l {
			t.Errorf("layer of %d = %d, want %d (all %v)", n, got[n], l, got)
		}
	}
}
