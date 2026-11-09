// Package land checks and lands approved pull requests at an exact combined tree.
package land

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// API is implemented by internal/client.Client, authenticated as the owner.
type API interface {
	Call(context.Context, string, string, any, any) error
}

// Kind selects the groups required at the landing commit.
type Kind int

const (
	Main      Kind = iota // check and e2e
	Milestone             // check only
)

// Policy supplies the repository, shared runs directory and check policy.
// Repair may run a flow after Conflict or NeedsVerify. It runs without the
// flow lock; true requests a fresh lock and full revalidation, false stops.
type Policy struct {
	Repo         string
	RunsDir      string
	Kind         Kind
	CheckTimeout time.Duration
	Repair       func(context.Context, Outcome) bool
	// Progress, when set, is told each step as it starts, so a caller can
	// show what a long landing is doing: one short line, without the PR.
	Progress func(step string)
}

// Outcome is a printable result. Callers may switch on its concrete type.
type Outcome interface{ fmt.Stringer }

// Position identifies the PR and the head at which an outcome occurred.
type Position struct {
	PR   int64
	Head string
}

func (p Position) line(reason string) string {
	return fmt.Sprintf("!%d at %s: %s", p.PR, p.Head, strings.Join(strings.Fields(reason), " "))
}

type Landed struct {
	Position
	MergeCommit string
}

func (o Landed) String() string { return o.line("landed " + o.MergeCommit) }

type Conflict struct {
	Position
	Files []string
}

func (o Conflict) String() string { return o.line("conflict: " + strings.Join(o.Files, ", ")) }

type NeedsVerify struct{ Position }

func (o NeedsVerify) String() string { return o.line("needs verify: approval did not carry") }

type ChecksFailed struct {
	Position
	Log, Description string
}

func (o ChecksFailed) String() string {
	return o.line("checks failed: " + o.Description + "; log: " + o.Log)
}

type BaseMoved struct{ Position }

func (o BaseMoved) String() string { return o.line("base moved after three retries") }

// Refused covers invalid candidates and operational errors; Err preserves the cause.
type Refused struct {
	Position
	Err error
}

func (o Refused) String() string { return o.line("refused: " + o.Err.Error()) }

func issueNumber(branch string) int64 {
	s, ok := strings.CutPrefix(branch, "issue/")
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

// Land holds the issue's flow lock across validation, landing, checks and
// merge. A moved base rebuilds and rechecks the landing, at most three
// retries after the initial attempt. No check result is reused.
func Land(ctx context.Context, api API, checker flow.Checker, policy Policy, pr server.PROut) Outcome {
	pos := Position{pr.Number, pr.HeadSHA}
	refuse := func(err error) Outcome { return Refused{pos, err} }
	progress := func(format string, args ...any) {
		if policy.Progress != nil {
			policy.Progress(fmt.Sprintf(format, args...))
		}
	}
	if policy.Repo == "" || policy.RunsDir == "" || pr.Number <= 0 || checker == nil || (policy.Kind != Main && policy.Kind != Milestone) {
		return refuse(errors.New("repo, runs directory, PR, checker and valid policy are required"))
	}
	timeout := policy.CheckTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	target := server.Target{Repo: policy.Repo, Number: pr.Number}
	query := "?" + url.Values{"repo": {policy.Repo}, "number": {fmt.Sprint(pr.Number)}}.Encode()
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return refuse(err)
		}
		issue := issueNumber(pr.HeadBranch)
		if issue > 0 {
			var err error
			release, err = runs.HoldFlow(policy.RunsDir, policy.Repo, issue)
			if err != nil {
				return refuse(err)
			}
		}
		var current server.PROut
		if err := api.Call(ctx, "GET", "/v1/forge/pr"+query, nil, &current); err != nil {
			return refuse(err)
		}
		pos.Head = current.HeadSHA
		if current.State != "open" || current.Merged || current.Assessment.Decision != review.Approved || current.BaseBranch != pr.BaseBranch || current.HeadBranch != pr.HeadBranch || current.Syncing {
			return refuse(errors.New("PR must remain open, approved, synced and on its original base and head branch"))
		}
		var live []secrets.Run
		q := "?" + url.Values{"repo": {policy.Repo}, "state": {"live"}}.Encode()
		if err := api.Call(ctx, "GET", "/v1/runs"+q, nil, &live); err != nil {
			return refuse(err)
		}
		for _, r := range live {
			if r.Ended != nil || (!r.Expires.IsZero() && time.Now().After(r.Expires)) {
				continue
			}
			if r.PR == pr.Number || r.Branch == current.HeadBranch || (issue > 0 && r.Issue == issue) {
				return refuse(fmt.Errorf("run %s is live on the PR", r.ID))
			}
		}
		var outcome Outcome
		for attempt := 0; attempt <= 3; attempt++ {
			if attempt > 0 {
				var fresh server.PROut
				if err := api.Call(ctx, "GET", "/v1/forge/pr"+query, nil, &fresh); err != nil {
					return refuse(err)
				}
				if fresh.State != "open" || fresh.Merged || fresh.Syncing || fresh.Assessment.Decision != review.Approved || fresh.BaseBranch != pr.BaseBranch || fresh.HeadBranch != pr.HeadBranch || fresh.HeadSHA != pos.Head {
					return refuse(errors.New("PR changed during landing"))
				}
			}
			var status server.PRMergeStatus
			if err := api.Call(ctx, "GET", "/v1/forge/pr/merge-base"+query, nil, &status); err != nil {
				return refuse(err)
			}
			if status.HeadSHA != pos.Head {
				return refuse(errors.New("PR head moved during landing"))
			}
			if !status.Clean {
				outcome = Conflict{pos, status.Conflicts}
				break
			}
			if attempt > 0 {
				progress("base moved; building the landing commit again (retry %d of 3)", attempt)
			} else {
				progress("building the landing commit: %s into %s", pr.BaseBranch, short(pos.Head))
			}
			var landing server.PRLandingOut
			err := api.Call(ctx, "POST", "/v1/forge/pr/landing", server.PRLandingIn{Target: target, HeadSHA: pos.Head}, &landing)
			if err != nil {
				// A conflict may appear after the trial read if the base moved.
				// Classify it from structured state, never from error text.
				var apiErr *client.APIError
				if errors.As(err, &apiErr) && apiErr.Status == 409 {
					var latest server.PRMergeStatus
					if readErr := api.Call(ctx, "GET", "/v1/forge/pr/merge-base"+query, nil, &latest); readErr == nil && latest.HeadSHA == pos.Head && !latest.Clean {
						outcome = Conflict{pos, latest.Conflicts}
						break
					}
				}
				return refuse(err)
			}
			pos.Head = landing.HeadSHA
			if landing.Assessment.Decision != review.Approved {
				outcome = NeedsVerify{pos}
				break
			}
			groups := []string{flow.GroupCheck}
			if policy.Kind == Main {
				groups = append(groups, flow.GroupE2E)
			}
			progress("running %s at %s", strings.Join(groups, " and "), short(pos.Head))
			checked, err := flow.RunCheck(ctx, checker, flow.CheckRequest{Repo: policy.Repo, Issue: issue, PR: pr.Number, SHA: pos.Head, Groups: groups}, timeout)
			if err != nil {
				return refuse(err)
			}
			if err := flow.RecordChecks(ctx, recorder{api}, policy.Repo, pos.Head, checked); err != nil {
				return refuse(err)
			}
			if !checked.Passed && !checked.Skipped {
				return ChecksFailed{pos, checked.Log, checked.Description}
			}
			verdict := "passed"
			if !checked.Passed {
				verdict = "skipped"
			}
			progress("checks %s (%s); merging %s", verdict, checked.Description, short(pos.Head))
			var merged server.LandOut
			err = api.Call(ctx, "POST", "/v1/forge/pr/merge", server.LandIn{Target: target, HeadSHA: pos.Head}, &merged)
			if err == nil {
				return Landed{pos, merged.MergeCommit}
			}
			if !errors.Is(err, client.ErrBaseMoved) {
				return refuse(err)
			}
			if attempt == 3 {
				return BaseMoved{pos}
			}
		}
		if policy.Repair == nil {
			return outcome
		}
		if release != nil {
			release()
			release = nil
		}
		if !policy.Repair(ctx, outcome) {
			return outcome
		}
	}
}

// short abbreviates a commit SHA for a progress line.
func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

type recorder struct{ API }

func (r recorder) RecordGate(ctx context.Context, repo, sha, check, state, description string) error {
	return r.Call(ctx, "POST", "/v1/forge/status", server.CommitStatusIn{Repo: repo, SHA: sha, Check: check, State: state, Description: description}, nil)
}
