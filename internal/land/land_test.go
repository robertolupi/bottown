package land

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

type fakeAPI struct {
	before     func(string)
	landingErr error
	mergeErr   error

	t                                *testing.T
	policy                           Policy
	pr                               server.PROut
	conflict, lost, live             bool
	moved, landings, merges, records int
	failPath                         string
}

func (a *fakeAPI) Call(_ context.Context, method, path string, in, out any) error {
	if _, held := runs.FlowHolder(a.policy.RunsDir, a.policy.Repo, 9); issueNumber(a.pr.HeadBranch) > 0 && !held {
		a.t.Fatal("API call outside lock", path)
	}
	path = strings.Split(path, "?")[0]
	if a.before != nil {
		a.before(path)
	}
	if path == a.failPath {
		return errors.New("offline")
	}
	switch path {
	case "/v1/forge/pr":
		*out.(*server.PROut) = a.pr
	case "/v1/runs":
		if a.live {
			*out.(*[]secrets.Run) = []secrets.Run{{ID: "live", Issue: 9}}
		}
	case "/v1/forge/pr/merge-base":
		*out.(*server.PRMergeStatus) = server.PRMergeStatus{HeadSHA: a.pr.HeadSHA, Clean: !a.conflict, Conflicts: []string{"a.go"}}
	case "/v1/forge/pr/landing":
		if a.landingErr != nil {
			return a.landingErr
		}
		if in.(server.PRLandingIn).HeadSHA != a.pr.HeadSHA {
			a.t.Fatal("wrong landing head")
		}
		a.landings++
		a.pr.HeadSHA = fmt.Sprint("landing", a.landings)
		decision := review.Approved
		if a.lost {
			decision = review.NeedsReview
		}
		*out.(*server.PRLandingOut) = server.PRLandingOut{HeadSHA: a.pr.HeadSHA, Assessment: review.Assessment{Decision: decision}}
	case "/v1/forge/status":
		status := in.(server.CommitStatusIn)
		if status.SHA != a.pr.HeadSHA {
			a.t.Fatal("status at wrong head")
		}
		a.records++
	case "/v1/forge/pr/merge":
		a.merges++
		if a.mergeErr != nil {
			return a.mergeErr
		}
		if in.(server.LandIn).HeadSHA != a.pr.HeadSHA {
			a.t.Fatal("merge at wrong head")
		}
		if a.merges <= a.moved {
			return &client.APIError{Code: server.ErrorCodeBaseMoved}
		}
		*out.(*server.LandOut) = server.LandOut{MergeCommit: a.pr.HeadSHA}
	default:
		a.t.Fatal(method, path)
	}
	return nil
}

type fakeChecker struct {
	api      *fakeAPI
	fail     bool
	requests []flow.CheckRequest
}

func (c *fakeChecker) LogPath(flow.CheckRequest) string { return "checks.log" }
func (c *fakeChecker) Check(_ context.Context, r flow.CheckRequest) (flow.CheckOutcome, error) {
	if _, held := runs.FlowHolder(c.api.policy.RunsDir, c.api.policy.Repo, 9); issueNumber(c.api.pr.HeadBranch) > 0 && !held {
		c.api.t.Fatal("checker outside lock")
	}
	if r.SHA != c.api.pr.HeadSHA {
		c.api.t.Fatal("checking wrong commit")
	}
	c.requests = append(c.requests, r)
	o := flow.CheckOutcome{Passed: !c.fail, Log: "checks.log", Description: "make check"}
	for _, g := range r.Groups {
		o.Groups = append(o.Groups, flow.GroupOutcome{Group: g, Passed: !c.fail})
	}
	return o, nil
}
func fixture(t *testing.T) (*fakeAPI, *fakeChecker) {
	a := &fakeAPI{t: t, policy: Policy{Repo: "demo", RunsDir: t.TempDir()}, pr: server.PROut{Number: 12, State: "open", HeadBranch: "issue/9", HeadSHA: "head", BaseBranch: "main", Assessment: review.Assessment{Decision: review.Approved}}}
	return a, &fakeChecker{api: a}
}
func TestOutcomes(t *testing.T) {
	for _, name := range []string{"landed", "conflict", "verify", "checks", "base moved", "live", "closed", "unapproved", "base changed", "API error", "milestone", "retry"} {
		t.Run(name, func(t *testing.T) {
			a, c := fixture(t)
			original := a.pr
			var want Outcome = Landed{}
			switch name {
			case "conflict":
				a.conflict = true
				want = Conflict{}
			case "verify":
				a.lost = true
				want = NeedsVerify{}
			case "checks":
				c.fail = true
				want = ChecksFailed{}
			case "base moved":
				a.moved = 10
				want = BaseMoved{}
			case "live":
				a.live = true
				want = Refused{}
			case "closed":
				a.pr.State = "closed"
				want = Refused{}
			case "unapproved":
				a.pr.Assessment.Decision = review.NeedsReview
				want = Refused{}
			case "base changed":
				a.pr.BaseBranch = "other"
				want = Refused{}
			case "API error":
				a.failPath = "/v1/forge/pr/landing"
				want = Refused{}
			case "milestone":
				a.policy.Kind = Milestone
			case "retry":
				a.moved = 2
			}
			got := Land(context.Background(), a, c, a.policy, original)
			if reflect.TypeOf(got) != reflect.TypeOf(want) {
				t.Fatalf("got %T: %s, want %T", got, got, want)
			}
			if strings.Contains(got.String(), "\n") || !strings.Contains(got.String(), "!12 at ") {
				t.Fatal(got.String())
			}
			if name == "base moved" && (a.merges != 4 || len(c.requests) != 4) {
				t.Fatal("retry bound", a.merges, len(c.requests))
			}
			if name == "retry" && a.merges != 3 {
				t.Fatal(a.merges)
			}
			if name == "landed" || name == "milestone" {
				groups := []string{"check", "e2e"}
				if name == "milestone" {
					groups = groups[:1]
				}
				if !reflect.DeepEqual(c.requests[0].Groups, groups) || a.records != len(groups) {
					t.Fatal(c.requests, a.records)
				}
			}
			if _, held := runs.FlowHolder(a.policy.RunsDir, a.policy.Repo, 9); held {
				t.Fatal("lock leaked")
			}
		})
	}
}
func TestRepair(t *testing.T) {
	for _, lost := range []bool{false, true} {
		for _, repair := range []bool{false, true} {
			t.Run(fmt.Sprint(lost, repair), func(t *testing.T) {
				a, c := fixture(t)
				a.lost = lost
				a.conflict = !lost
				called := 0
				a.policy.Repair = func(_ context.Context, o Outcome) bool {
					called++
					release, err := runs.HoldFlow(a.policy.RunsDir, a.policy.Repo, 9)
					if err != nil {
						t.Fatal("repair lock held", err)
					}
					release()
					a.conflict = false
					a.lost = false
					a.pr.HeadSHA = "repaired"
					return repair
				}
				got := Land(context.Background(), a, c, a.policy, a.pr)
				if called != 1 {
					t.Fatal(called)
				}
				_, landed := got.(Landed)
				if landed != repair {
					t.Fatal(got)
				}
			})
		}
	}
}
func TestBusy(t *testing.T) {
	a, c := fixture(t)
	release, err := runs.HoldFlow(a.policy.RunsDir, a.policy.Repo, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	got := Land(context.Background(), a, c, a.policy, a.pr)
	r, ok := got.(Refused)
	var busy *runs.FlowBusyError
	if !ok || !errors.As(r.Err, &busy) {
		t.Fatal(got)
	}
}
func TestRepairRevalidates(t *testing.T) {
	a, c := fixture(t)
	a.conflict = true
	a.policy.Repair = func(context.Context, Outcome) bool { a.pr.BaseBranch = "retargeted"; return true }
	if got := Land(context.Background(), a, c, a.policy, a.pr); reflect.TypeOf(got) != reflect.TypeOf(Refused{}) {
		t.Fatal(got)
	}
}

func TestConflictDuringLanding(t *testing.T) {
	a, c := fixture(t)
	a.landingErr = &client.APIError{Status: 409}
	a.before = func(path string) {
		if path == "/v1/forge/pr/landing" {
			a.conflict = true
		}
	}
	got := Land(context.Background(), a, c, a.policy, a.pr)
	conflict, ok := got.(Conflict)
	if !ok || !reflect.DeepEqual(conflict.Files, []string{"a.go"}) || len(c.requests) != 0 {
		t.Fatal(got, c.requests)
	}
}

func TestHeadMovesAndMergeErrors(t *testing.T) {
	for _, where := range []string{"trial", "landing", "merge", "retry retarget"} {
		t.Run(where, func(t *testing.T) {
			a, c := fixture(t)
			switch where {
			case "trial":
				a.before = func(path string) {
					if path == "/v1/forge/pr/merge-base" {
						a.pr.HeadSHA = "other"
					}
				}
			case "landing":
				a.landingErr = &client.APIError{Status: 409, Code: server.ErrorCodeHeadMoved}
			case "merge":
				a.mergeErr = errors.New("merge unavailable")
			case "retry retarget":
				a.moved = 1
				a.before = func(path string) {
					if path == "/v1/forge/pr" && a.merges > 0 {
						a.pr.BaseBranch = "other"
					}
				}
			}
			got := Land(context.Background(), a, c, a.policy, a.pr)
			if _, ok := got.(Refused); !ok {
				t.Fatal(got)
			}
			if a.merges > 1 {
				t.Fatal("retried an unsafe operation", a.merges)
			}
		})
	}
}

func TestFinalMilestonePR(t *testing.T) {
	a, c := fixture(t)
	a.pr.HeadBranch = "milestone/test"
	// The issue's lock must not affect a final milestone PR.
	release, err := runs.HoldFlow(a.policy.RunsDir, a.policy.Repo, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	got := Land(context.Background(), a, c, a.policy, a.pr)
	if _, ok := got.(Landed); !ok {
		t.Fatal(got)
	}
	if c.requests[0].Issue != 0 {
		t.Fatal(c.requests)
	}
}

func TestOutcomeStrings(t *testing.T) {
	pos := Position{12, "abc"}
	outcomes := []Outcome{Landed{pos, "def"}, Conflict{pos, []string{"a.go", "b.go"}}, NeedsVerify{pos}, ChecksFailed{pos, "/tmp/check.log", "failed\nmake check"}, BaseMoved{pos}, Refused{pos, errors.New("offline\nretry later")}}
	for _, o := range outcomes {
		text := o.String()
		if !strings.HasPrefix(text, "!12 at abc: ") || strings.Contains(text, "\n") {
			t.Fatal(text)
		}
	}
	if text := outcomes[3].String(); !strings.Contains(text, "/tmp/check.log") || !strings.Contains(text, "failed make check") {
		t.Fatal(text)
	}
}
