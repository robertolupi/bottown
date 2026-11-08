package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/server"
)

func TestChildPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/forge/prs":
			fmt.Fprint(w, `[{"number":3,"state":"closed","merged":true,"head_branch":"issue/7","head_sha":"old"},
				{"number":4,"state":"open","head_branch":"issue/8","head_sha":"other"},
				{"number":5,"state":"open","head_branch":"issue/7","head_sha":"cur"},
				{"number":6,"state":"closed","head_branch":"issue/9","head_sha":"x"},
				{"number":7,"state":"closed","merged":true,"head_branch":"issue/10","head_sha":"done"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := client.New(srv.URL, "k")
	for _, tc := range []struct {
		issue      int64
		wantNumber int64
		wantMerged bool
	}{
		{7, 5, false}, // the open PR wins over the merged one
		{8, 4, false},
		{10, 7, true}, // merged, none open
	} {
		pr, err := childPR(t.Context(), c, "demo", tc.issue)
		if err != nil || pr.Number != tc.wantNumber || pr.Merged != tc.wantMerged {
			t.Errorf("#%d: %+v, %v; want !%d merged %v", tc.issue, pr, err, tc.wantNumber, tc.wantMerged)
		}
	}
	if _, err := childPR(t.Context(), c, "demo", 9); err == nil {
		t.Error("#9 (closed, unmerged PR): no error")
	}
}

// milestoneFake is the merge-all fake server with two children of
// milestone/demo: #7's !1 and #8's !2.
func milestoneFake(t *testing.T) *fakeForge {
	f := newFakeForge(t)
	f.add(&fakePR{number: 1, author: "amy", headBranch: "issue/7", headSHA: "h1", baseBranch: "milestone/demo"})
	f.add(&fakePR{number: 2, author: "amy", headBranch: "issue/8", headSHA: "h2", baseBranch: "milestone/demo"})
	return f
}

func landChild(t *testing.T, f *fakeForge, checker flow.Checker, issue int64, sha string) (milestone.Landing, error) {
	t.Helper()
	return milestoneLand(t.Context(), f.client(), checker, t.TempDir(), time.Minute, "demo", issue, sha)
}

// A child landing behind a sibling lands at the landing commit, checked
// with the check group only.
func TestMilestoneLandChecksCombinedHead(t *testing.T) {
	f := milestoneFake(t)
	checker := &okChecker{passes: true}
	if l, err := landChild(t, f, checker, 7, ""); err != nil || l.Kind != milestone.Landed {
		t.Fatalf("#7: %+v, %v", l, err)
	}
	f.prs[2].behind = true // #7 landed: milestone/demo moved past !2's head
	l, err := landChild(t, f, checker, 8, "h2")
	if err != nil || l.Kind != milestone.Landed {
		t.Fatalf("#8: %+v, %v", l, err)
	}
	if want := (milestone.Merge{PR: 2, HeadSHA: "land-2-2", Base: "milestone/demo"}); l.Merge != want {
		t.Errorf("merge %+v, want %+v", l.Merge, want)
	}
	if !slices.Equal(f.landings, []string{"!1@h1", "!2@h2"}) || !slices.Equal(f.merges, []string{"!1@land-1-1", "!2@land-2-2"}) {
		t.Errorf("landings %v, merges %v", f.landings, f.merges)
	}
	if len(checker.requests) != 2 {
		t.Fatalf("checks %+v", checker.requests)
	}
	if r := checker.requests[1]; r.SHA != "land-2-2" || r.Issue != 8 || !slices.Equal(r.Groups, []string{flow.GroupCheck}) {
		t.Errorf("check %+v: want the check group alone, at the landing commit", r)
	}
	if !slices.Equal(f.statuses, []string{"check@land-1-1:success", "check@land-2-2:success"}) {
		t.Errorf("statuses %v", f.statuses)
	}
}

// A conflict and a lost approval go back to the flow; failed checks stop
// with the log; a head other than --sha lands nothing.
func TestMilestoneLandOutcomes(t *testing.T) {
	f := milestoneFake(t)
	f.conflicts[1] = []string{"x.txt"}
	f.lost[2] = true
	if l, err := landChild(t, f, &okChecker{passes: true}, 7, ""); err != nil || l.Kind != milestone.LandRepair || !strings.Contains(l.Reason, "conflict: x.txt") {
		t.Errorf("conflict: %+v, %v", l, err)
	}
	if l, err := landChild(t, f, &okChecker{passes: true}, 8, ""); err != nil || l.Kind != milestone.LandRepair || !strings.Contains(l.Reason, "needs verify") {
		t.Errorf("lost approval: %+v, %v", l, err)
	}
	delete(f.conflicts, 1)
	if l, err := landChild(t, f, &okChecker{passes: false}, 7, ""); err != nil || l.Kind != milestone.LandChecksFailed || !strings.Contains(l.Reason, "log: checks.log") {
		t.Errorf("checks failed: %+v, %v", l, err)
	}
	if len(f.merges) != 0 {
		t.Errorf("merged %v", f.merges)
	}
	head := f.prs[1].headSHA
	if _, err := landChild(t, f, &okChecker{passes: true}, 7, "elsewhere"); err == nil || !strings.Contains(err.Error(), "not elsewher") {
		t.Errorf("--sha elsewhere: %v", err)
	}
	if f.prs[1].headSHA != head || len(f.merges) != 0 {
		t.Errorf("--sha mismatch landed: head %s, merges %v", f.prs[1].headSHA, f.merges)
	}
}

// A merged PR whose child is still open, because the call that merged it
// stopped before closing it, is finished by the next call: the landing
// merge again, with no checks. Its failure is reported, not a landing.
func TestMilestoneLandFinishesMergedChild(t *testing.T) {
	var merges []server.LandIn
	closed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/forge/prs":
			fmt.Fprint(w, `[{"number":3,"state":"closed","merged":true,"head_branch":"issue/7","head_sha":"h3","base_branch":"milestone/demo"}]`)
		case "POST /v1/forge/pr/merge":
			var in server.LandIn
			_ = json.NewDecoder(r.Body).Decode(&in)
			merges = append(merges, in)
			if len(merges) == 1 {
				http.Error(w, `{"error":"close child issue #7: forge unavailable"}`, http.StatusBadGateway)
				return
			}
			out := server.LandOut{Number: 3, Issue: 7, Base: "milestone/demo", HeadSHA: "h3", AlreadyMerged: true, IssueClosed: !closed}
			closed = true
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	land := func() (milestone.Landing, error) {
		return milestoneLand(t.Context(), client.New(srv.URL, "k"), &okChecker{passes: false}, t.TempDir(), time.Minute, "demo", 7, "")
	}
	if l, err := land(); err == nil || !strings.Contains(err.Error(), "finishing !3's merge") {
		t.Errorf("failed closure: %+v, %v; want an error", l, err)
	}
	l, err := land()
	if err != nil || l.Kind != milestone.Landed || !strings.Contains(l.Reason, "already merged; closed #7") {
		t.Fatalf("retry: %+v, %v", l, err)
	}
	if want := (milestone.Merge{PR: 3, HeadSHA: "h3", Base: "milestone/demo"}); l.Merge != want {
		t.Errorf("merge %+v, want %+v", l.Merge, want)
	}
	if l, err := land(); err != nil || l.Kind != milestone.Landed || strings.Contains(l.Reason, "closed") {
		t.Errorf("third call: %+v, %v", l, err)
	}
	if len(merges) != 3 || merges[1].Number != 3 || merges[1].HeadSHA != "h3" || merges[1].Repo != "demo" {
		t.Errorf("merge calls %+v", merges)
	}
}

// Order orders the children by their PRs' trial merges and overlaps.
func TestRunForgeOrder(t *testing.T) {
	f := milestoneFake(t)
	f.add(&fakePR{number: 3, author: "amy", headBranch: "issue/9", headSHA: "h3", baseBranch: "milestone/demo"})
	f.conflicts[1] = []string{"a.txt"}      // conflicting: last
	f.files[2] = []string{"b.txt", "c.txt"} // overlaps !3
	f.files[3] = []string{"c.txt"}
	f.files[1] = []string{"z.txt"}
	rf := &runForge{proxyForge: &proxyForge{repo: "demo"}, c: f.client()}
	got, err := rf.Order(t.Context(), []int64{7, 8, 9})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int64{8, 9, 7}) {
		t.Errorf("order %v, want #8 #9 #7", got)
	}
}
