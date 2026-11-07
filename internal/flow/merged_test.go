package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/server"
)

// merge is the owner merging the flow's PR at head, which closes the issue.
func merge(api *fakeAPI, head string) {
	api.pr.HeadSHA, api.pr.Merged, api.pr.State = head, true, "closed"
	api.open, api.issueState = false, "closed"
}

// The owner merges the PR while an implement round runs: the flow stops as
// merged, not run failed, and starts no other run.
func TestFlowStopsMergedMidFlow(t *testing.T) {
	requestChanges := func(api *fakeAPI) {
		api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: 1, Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: api.pr.HeadSHA})
	}
	for _, tc := range []struct {
		name    string
		setup   func(*fakeAPI)
		checker *fakeChecker
		// pushed is whether the implement round pushed before the merge.
		pushed bool
	}{
		// The round answering failed checks continues the loop, which finds
		// no open PR: the case of #326, where the next run was refused.
		{name: "after a check round", checker: &fakeChecker{check: failing}, pushed: true},
		{name: "after a review round", setup: requestChanges, pushed: true},
		{name: "during a review round that pushed nothing", setup: requestChanges},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{open: true, pr: openPR()}
			if tc.setup != nil {
				tc.setup(api)
			}
			var reqs []RunRequest
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				reqs = append(reqs, req)
				head := api.pr.HeadSHA
				if tc.pushed {
					head = "h2"
				}
				merge(api, head)
				return nil
			})
			s := &Supervisor{API: api, Runner: runner}
			if tc.checker != nil {
				s.Checker = tc.checker
			}
			result, err := s.Run(context.Background(), gateOptions())
			var stop *StopError
			if !errors.As(err, &stop) || result.Reason != Merged {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if kinds(reqs) != policy.Implement {
				t.Fatalf("runs = %s, want the one implement round", kinds(reqs))
			}
			head := "h1"
			if tc.pushed {
				head = "h2"
			}
			want := fmt.Sprintf("!12 was merged at %s; nothing left to do", head)
			last := api.comments[len(api.comments)-1]
			if result.Detail != want || !strings.HasPrefix(last, "**bottown flow: merged**") || strings.Contains(last, "run failed") {
				t.Fatalf("detail = %q, comment = %q", result.Detail, last)
			}
		})
	}
}

// A closed issue, before the flow starts any run, is a merged stop too.
func TestFlowStopsMergedIssueClosed(t *testing.T) {
	api := &fakeAPI{issueState: "closed"}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("the flow started a run on a closed issue")
		return nil
	})
	result, _ := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if result.Reason != Merged || result.Detail != "issue #9 is closed; nothing left to do" {
		t.Fatalf("result = %+v", result)
	}
}

// The merge lands between the flow's read and the run's creation: the
// server's refusal of the run on the closed issue is a merged stop.
func TestFlowStopsMergedOnClosedIssueRefusal(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR()}
	api.pr.Reviews = []server.ReviewOut{{ID: 1, Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: "h1"}}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		merge(api, "h1")
		return fmt.Errorf("%w: instance prod: POST /v1/runs: issue #9 is closed", ErrIssueClosed)
	})
	result, _ := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if result.Reason != Merged || result.Detail != "!12 was merged at h1; nothing left to do" {
		t.Fatalf("result = %+v", result)
	}
}
