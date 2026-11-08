package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/server"
)

type fakeMilestoneAPI struct {
	merge    server.MilestoneMergeBaseOut
	mergeErr error
	prs      []server.MilestonePRIn
	comments []string
}

func (f *fakeMilestoneAPI) MergeBase(_ context.Context, in server.MilestoneIn) (server.MilestoneMergeBaseOut, error) {
	return f.merge, f.mergeErr
}

func (f *fakeMilestoneAPI) FinalPR(_ context.Context, in server.MilestonePRIn) (server.MilestonePROut, error) {
	f.prs = append(f.prs, in)
	return server.MilestonePROut{Number: 40, URL: "http://forge/pulls/40", Created: len(f.prs) == 1}, nil
}

func (f *fakeMilestoneAPI) Comment(_ context.Context, repo string, n int64, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

type fakeChecker struct {
	out  flow.CheckOutcome
	reqs []flow.CheckRequest
}

func (c *fakeChecker) Check(_ context.Context, req flow.CheckRequest) (flow.CheckOutcome, error) {
	c.reqs = append(c.reqs, req)
	return c.out, nil
}

func (c *fakeChecker) LogPath(flow.CheckRequest) string { return "" }

const finishHead = "abcdef0123456789abcdef0123456789abcdef01"

func newFinishAPI() *fakeMilestoneAPI {
	return &fakeMilestoneAPI{merge: server.MilestoneMergeBaseOut{Branch: "milestone/demo", Base: "main", HeadSHA: finishHead, Epic: 7, Merged: true}}
}

// The checks run at the merged head, as the epic's; a failure comments on
// the epic with the summary and the log, and opens no PR.
func TestMilestoneFinishGateFails(t *testing.T) {
	api := newFinishAPI()
	checker := &fakeChecker{out: flow.CheckOutcome{Description: "make e2e failed", Summary: "make e2e failed (exit 2)\n    12  FAIL TestX", Log: "/runs/flow-7-abc.e2e.log"}}
	var out strings.Builder
	err := milestoneFinish(context.Background(), &out, api, checker, time.Minute, "demo", "demo")
	if err == nil || !strings.Contains(err.Error(), "/runs/flow-7-abc.e2e.log") {
		t.Fatalf("err = %v, want the failure with the log", err)
	}
	// Finish runs the e2e group alone.
	if len(checker.reqs) != 1 || !reflect.DeepEqual(checker.reqs[0], flow.CheckRequest{Repo: "demo", Issue: 7, SHA: finishHead, Groups: []string{flow.GroupE2E}}) {
		t.Errorf("checks %+v", checker.reqs)
	}
	if len(api.prs) != 0 {
		t.Errorf("opened a PR: %+v", api.prs)
	}
	if len(api.comments) != 1 || !strings.Contains(api.comments[0], "FAIL TestX") || !strings.Contains(api.comments[0], "/runs/flow-7-abc.e2e.log") {
		t.Errorf("comments %q", api.comments)
	}
}

// Passed checks open the PR at the checked head, and a second finish, after
// a merge moved the head, re-gates and updates it, and says on it that the
// head is gated.
func TestMilestoneFinishGatePasses(t *testing.T) {
	api := newFinishAPI()
	checker := &fakeChecker{out: flow.CheckOutcome{Passed: true, Description: "make e2e passed in 3m", Log: "/runs/l.e2e.log"}}
	for i, want := range []string{"opened !40", "updated !40"} {
		var out strings.Builder
		if err := milestoneFinish(context.Background(), &out, api, checker, time.Minute, "demo", "demo"); err != nil {
			t.Fatalf("finish %d: %v", i, err)
		}
		if !strings.Contains(out.String(), want) {
			t.Errorf("finish %d: output lacks %q:\n%s", i, want, out.String())
		}
	}
	want := server.MilestonePRIn{MilestoneIn: server.MilestoneIn{Repo: "demo", Slug: "demo"}, SHA: finishHead, Checks: "make e2e passed in 3m"}
	if len(api.prs) != 2 || api.prs[0] != want {
		t.Errorf("PRs %+v, want %+v", api.prs, want)
	}
	if len(checker.reqs) != 2 {
		t.Errorf("checks %+v: each finish gates the head", checker.reqs)
	}
	if len(api.comments) != 1 || !strings.Contains(api.comments[0], "the head is gated") || !strings.Contains(api.comments[0], finishHead) {
		t.Errorf("comments %q, want one on the updated PR", api.comments)
	}
}

// A refused merge (open children, a conflict) runs no checks.
func TestMilestoneFinishRefused(t *testing.T) {
	api := newFinishAPI()
	api.mergeErr = errors.New("epic #7 has 1 open active child issue(s): #4 b")
	checker := &fakeChecker{}
	err := milestoneFinish(context.Background(), &strings.Builder{}, api, checker, time.Minute, "demo", "demo")
	if err == nil || !strings.Contains(err.Error(), "#4") || len(checker.reqs) != 0 || len(api.prs)+len(api.comments) != 0 {
		t.Errorf("err %v; checks %v, PRs %v, comments %v", err, checker.reqs, api.prs, api.comments)
	}
}
