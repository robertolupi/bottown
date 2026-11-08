// Package flow supervises one issue through implement and cross-family
// verification, rebuilding its position from the forge and run records.
package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// StopReason is why a flow stopped.
type StopReason string

const (
	ReadyToMerge StopReason = "ready to merge"
	OutOfRounds  StopReason = "out of rounds"
	RunFailed    StopReason = "run failed"
	// AgentDidNothing: a run's agent ended, whatever its exit status,
	// without a single tool call, the usual sign of an actor out of quota
	// or signed out.
	AgentDidNothing   StopReason = "agent did nothing"
	VerifierUndecided StopReason = "verifier undecided"
	// ConflictsWithBase: the PR conflicts with its base (main, or a
	// milestone child's milestone/<slug>), and the flow cannot resolve it.
	ConflictsWithBase StopReason = "conflicts with base"
	// ChangesRequested: the head is approved, but a request for changes at
	// an older head still blocks the merge.
	ChangesRequested StopReason = "changes requested"
	// ChecksFailed: the post-implement checks failed at the head and no
	// check rounds remain.
	ChecksFailed StopReason = "checks failed"
	// ActorBusy: a flow that serialises actors waited Options.ActorWait
	// for an actor's other batch run to end, and it is still live.
	ActorBusy StopReason = "actor busy"
	// Merged: the PR was merged, or the issue closed, while the flow ran;
	// the work has landed and nothing is left to do.
	Merged StopReason = "merged"
)

// ErrActorBusy marks a run creation refused because the flow serialises
// actors (Options.SerializeActors) and the actor has another live batch
// run.
var ErrActorBusy = errors.New("actor has another live batch run")

// ErrIssueClosed marks an implement run creation refused because the issue
// is closed: the PR was merged, or the issue closed, after the flow last
// read them.
var ErrIssueClosed = errors.New("issue is closed")

// maxRetryDelay bounds the wait between tries of a run whose actor is busy.
const maxRetryDelay = time.Minute

// ErrHeadMoved marks a verify run creation refused because the PR's head is
// no longer the SHA the flow asked for: what the flow gated is not what the
// verifier would review.
var ErrHeadMoved = errors.New("pull request head moved")

// Options selects the issue, actors, and budgets for one flow. Implement
// and Verify are ACTOR[:SPEC] (runs.SplitActorSpec): the spec, a model
// choice, goes to the run unchanged and never changes the actor's family.
//
// Rounds bounds the review rounds: the flow stops at the Rounds-th request
// for changes. CheckRounds bounds the implement runs that answer failed
// post-implement checks, which do not use up Rounds. CheckTimeout limits
// each post-implement check, apart from Timeout, which limits each run.
type Options struct {
	Issue        int64
	Repo         string
	Implement    string
	Verify       string
	Rounds       int
	CheckRounds  int
	Timeout      time.Duration
	CheckTimeout time.Duration
	// SerializeActors starts a run only when its actor has no other live
	// batch run, waiting for it otherwise. By default an actor's runs on
	// different targets run concurrently.
	SerializeActors bool
	// ActorWait bounds each such wait: the flow stops (ActorBusy) when the
	// actor is still busy after it. It must be positive when
	// SerializeActors is set.
	ActorWait time.Duration
	Attended  bool

	round int   // the review round the PR is in, for RunRequest.Round
	pr    int64 // the flow's PR, once it has one, for landed
}

// Result is the flow's final position.
type Result struct {
	Issue  int64
	Repo   string
	PR     int64
	Reason StopReason
	Detail string
	// Actor is the actor whose agent did nothing, for an AgentDidNothing
	// stop, whose marker carries it.
	Actor string
}

// StopError is returned for every ordinary stop other than ready to merge.
type StopError struct{ Result Result }

func (e *StopError) Error() string {
	if e.Result.Detail != "" {
		return fmt.Sprintf("flow #%d stopped: %s (%s)", e.Result.Issue, e.Result.Reason, e.Result.Detail)
	}
	return fmt.Sprintf("flow #%d stopped: %s", e.Result.Issue, e.Result.Reason)
}

// RunRequest is one batch run requested by the supervisor.
type RunRequest struct {
	Kind  string
	Repo  string
	Actor string // ACTOR[:SPEC], as given in Options
	Issue int64
	PR    int64
	// SerializeActor asks the server to refuse the run while its actor
	// has another live batch run; the Runner then fails with ErrActorBusy.
	SerializeActor bool
	// Retry marks a try after one the server refused for a busy actor:
	// the Runner need not repeat what it prepared for that try, such as
	// fetching the mirror.
	Retry bool
	// Note is the flow's own note for the run's prompt: the failed checks
	// an implement round fixes, or the passed checks a verifier relies on.
	Note string
	// ChecksPassed tells a verify run the post-implement checks passed at
	// SHA, so it need not run them again.
	ChecksPassed bool
	// SHA, for a verify run, is the head the flow read and gated. The run
	// must be pinned there: the Runner fails with ErrHeadMoved if the PR's
	// head is another commit when the server creates the run.
	SHA string
	// Round and Rounds are where the flow stands, for display: the run's
	// review round, one more than the PR's requests for changes so far,
	// as the web UI counts it, out of Options.Rounds.
	Round, Rounds int
}

// CheckRequest is the post-implement checks at one PR head.
type CheckRequest struct {
	Repo  string
	Issue int64
	PR    int64
	SHA   string
}

// CheckOutcome is how the post-implement checks went.
type CheckOutcome struct {
	// Skipped: the repository names no post-implement checks.
	Skipped bool
	Passed  bool
	// Description is the commit status's: the commands and how long they
	// took, or which one failed. Summary adds the first failing lines.
	Description string
	Summary     string
	Log         string // the full log's path
}

// Checker runs the post-implement checks (the repository's e2e commands)
// in a clean worktree at a PR head, with no agent involved.
type Checker interface {
	Check(context.Context, CheckRequest) (CheckOutcome, error)
	// LogPath is where Check keeps req's log, for a resumed flow.
	LogPath(CheckRequest) string
}

// Runner executes a batch run and waits for it to finish.
type Runner interface {
	Run(context.Context, RunRequest) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, RunRequest) error

func (f RunnerFunc) Run(ctx context.Context, req RunRequest) error { return f(ctx, req) }

// API is the owner-authenticated state the supervisor reads and writes.
type API interface {
	Actors(context.Context) ([]server.ActorInfo, error)
	// Issue reads the issue alone, without its discussion.
	Issue(context.Context, string, int64) (server.IssueOut, error)
	OpenPR(context.Context, string, string) (server.PRItem, bool, error)
	PR(context.Context, string, int64) (server.PROut, error)
	// ReconcileCarries undismisses automatically dismissed approvals whose
	// patches carry over, so the next PR read reflects Forgejo's merge gate.
	ReconcileCarries(context.Context, string, int64) error
	LiveRuns(context.Context, string) ([]secrets.Run, error)
	Comment(context.Context, string, int64, string) error
	// RecordGate writes the post-implement checks' result at sha as the
	// flow's bottown/e2e commit status.
	RecordGate(ctx context.Context, repo, sha, state, description string) error
	// GateFailures lists the PR's commits where they failed.
	GateFailures(ctx context.Context, repo string, pr int64) ([]string, error)
	// PRMergeStatus trial-merges the PR's base into its head.
	PRMergeStatus(context.Context, string, int64) (server.PRMergeStatus, error)
}

// Supervisor drives one issue. RetryDelay is how long a flow that
// serialises actors first waits before trying a run whose actor is busy
// again; the wait doubles up to maxRetryDelay, within Options.ActorWait.
// Zero means one second.
// Checker, if set, gates each new head on the post-implement checks before
// a verifier sees it.
type Supervisor struct {
	API     API
	Runner  Runner
	Checker Checker
	In      io.Reader
	Out     io.Writer
	// Pause, if set, is called before a prompt that reads In, and the
	// function it returns after the answer: a live terminal tail gives
	// the terminal back for the prompt (internal/cli).
	Pause      func() (resume func())
	RetryDelay time.Duration
	// Lock, if set, takes the issue's flow lock for the flow's whole life
	// (internal/runs.HoldFlow), so a second flow on the issue refuses
	// rather than starting runs beside this one's.
	Lock func(repo string, issue int64) (release func(), err error)

	// now and after, if set, replace time.Now and time.After in the wait
	// for a busy actor (tests).
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// Run executes or resumes a flow. It returns nil only at ready to merge;
// ordinary non-success stops return StopError after commenting on the issue.
func (s *Supervisor) Run(ctx context.Context, opts Options) (Result, error) {
	result := Result{Issue: opts.Issue, Repo: opts.Repo}
	if opts.Issue <= 0 || opts.Repo == "" || opts.Implement == "" || opts.Verify == "" {
		return result, errors.New("issue, repo, implementer and verifier are required")
	}
	if opts.Rounds <= 0 {
		return result, errors.New("rounds must be positive")
	}
	if opts.Timeout <= 0 {
		return result, errors.New("timeout must be positive")
	}
	if opts.SerializeActors && opts.ActorWait <= 0 {
		return result, errors.New("the wait for a busy actor must be positive")
	}
	if s.Checker != nil && (opts.CheckRounds < 0 || opts.CheckTimeout <= 0) {
		return result, errors.New("check rounds must not be negative, and the check timeout must be positive")
	}
	if s.API == nil || s.Runner == nil {
		return result, errors.New("flow API and runner are required")
	}
	if s.Lock != nil {
		release, err := s.Lock(opts.Repo, opts.Issue)
		if err != nil {
			return result, err
		}
		defer release()
	}
	if err := s.checkFamilies(ctx, opts); err != nil {
		return result, err
	}
	resuming := true
	// fixing is the head whose failed checks the last implement round was
	// asked to fix: if it pushed nothing, the flow stops.
	fixing := ""
	// resolving is the head whose conflicts with the base the last
	// implement round was asked to resolve, and conflictRounds how many
	// such rounds this flow has started.
	resolving, conflictRounds := "", 0

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		item, open, err := s.API.OpenPR(ctx, opts.Repo, fmt.Sprintf("issue/%d", opts.Issue))
		if err != nil {
			return result, err
		}
		var pr server.PROut
		if open {
			pr, err = s.API.PR(ctx, opts.Repo, item.Number)
			if err != nil {
				return result, err
			}
			result.PR = pr.Number
		}
		opts.round = 1 + server.ChangesRequested(pr.Reviews)
		opts.pr = result.PR
		if err := s.checkLiveRuns(ctx, opts, pr.Number); err != nil {
			return result, err
		}
		if !open {
			if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue}); err != nil {
				return s.runFailure(ctx, result, err)
			}
			resuming = false
			_, open, err = s.API.OpenPR(ctx, opts.Repo, fmt.Sprintf("issue/%d", opts.Issue))
			if err != nil {
				return result, err
			}
			if !open {
				if detail, ok, err := s.landed(ctx, opts); err != nil {
					return result, err
				} else if ok {
					return s.stop(ctx, result, Merged, detail)
				}
				return s.stop(ctx, result, RunFailed, "the implement run ended without opening a PR")
			}
			continue
		}

		pr, err = s.waitForPush(ctx, opts, pr)
		if err != nil {
			return result, err
		}
		if autoDismissible(pr) {
			if err := s.API.ReconcileCarries(ctx, opts.Repo, pr.Number); err != nil {
				return result, err
			}
			pr, err = s.API.PR(ctx, opts.Repo, pr.Number)
			if err != nil {
				return result, err
			}
		}
		merge, err := s.API.PRMergeStatus(ctx, opts.Repo, pr.Number)
		if err != nil {
			return result, err
		}
		// A request for changes at the head is answered first: a conflict
		// round waits until the implement round pushes its fix, so the
		// rejected patch is addressed before resolving base conflicts.
		changesAtHead := pr.Assessment.Decision == review.ChangesRequested && pr.Assessment.AtHead(pr.HeadSHA)
		if changesAtHead {
			merge.Behind = false
		}
		if merge.Behind && !merge.Clean {
			files := strings.Join(merge.Conflicts, ", ")
			if files == "" {
				files = "unknown files"
			}
			// An implement round resolves the conflicts. These rounds are
			// the flow's own, bounded by Rounds; one that pushes nothing, or
			// conflicts that outlast the rounds, stop the flow.
			if resolving == pr.HeadSHA {
				return s.stop(ctx, result, ConflictsWithBase, fmt.Sprintf("the implement round to resolve the conflicts with %s at %s pushed no new commit; merging %s into %s for !%d would still conflict in %s",
					pr.BaseBranch, shortSHA(pr.HeadSHA), pr.BaseBranch, pr.HeadBranch, pr.Number, files))
			}
			if conflictRounds >= opts.Rounds {
				return s.stop(ctx, result, ConflictsWithBase, fmt.Sprintf("merging %s into %s for !%d would conflict in %s, and %d conflict rounds are used; start an implement run to resolve it",
					pr.BaseBranch, pr.HeadBranch, pr.Number, files, conflictRounds))
			}
			conflictRounds++
			if err := s.API.Comment(ctx, opts.Repo, opts.Issue, roundBody("conflicts with "+pr.BaseBranch, fmt.Sprintf("on !%d at %s, in %s; starting implement round %d of %d to resolve them.",
				pr.Number, shortSHA(pr.HeadSHA), files, conflictRounds, opts.Rounds))); err != nil {
				return result, err
			}
			resolving = pr.HeadSHA
			if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue, Note: conflictNote(pr, merge.Conflicts)}); err != nil {
				return s.runFailure(ctx, result, err)
			}
			resuming = false
			continue
		}
		if !pr.Mergeable && !(merge.Behind && merge.Clean) && !changesAtHead {
			if !resuming {
				return s.stop(ctx, result, ConflictsWithBase, fmt.Sprintf("!%d no longer merges cleanly into %s; rerun the flow to resolve it", pr.Number, pr.BaseBranch))
			}
			if server.ChangesRequested(pr.Reviews) >= opts.Rounds {
				return s.stop(ctx, result, OutOfRounds, fmt.Sprintf("the PR conflicts with %s and no implement rounds remain", pr.BaseBranch))
			}
			if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue}); err != nil {
				return s.runFailure(ctx, result, err)
			}
			resuming = false
			continue
		}

		// The server assesses the reviews (internal/review), so the flow,
		// the PR list and the inbox agree on where the PR stands.
		assessment := pr.Assessment
		switch {
		case assessment.Decision == review.Approved:
			detail := fmt.Sprintf("!%d is approved at %s", pr.Number, shortSHA(pr.HeadSHA))
			if assessment.Reason != "" {
				detail += ": " + assessment.Reason
			}
			if merge.Behind && merge.Clean {
				base := pr.BaseBranch
				if base == "" {
					base = "main"
				}
				detail += fmt.Sprintf(": behind `%s`, cleanly; `bottown merge-all` lands it", base)
			}
			return s.stop(ctx, result, ReadyToMerge, detail)
		case assessment.Decision == review.ChangesRequested && !assessment.AtHead(pr.HeadSHA):
			// A follow-up cannot clear an older head's request: only its
			// reviewer, reviewing again, or the owner, dismissing it.
			blocking := assessment.Review
			return s.stop(ctx, result, ChangesRequested, fmt.Sprintf("!%d is approved at %s, but %s's request for changes at %s still blocks it; %s must review again, or the owner dismisses it",
				pr.Number, shortSHA(pr.HeadSHA), blocking.Reviewer, shortSHA(blocking.CommitID), blocking.Reviewer))
		case assessment.Decision == review.ChangesRequested:
			if server.ChangesRequested(pr.Reviews) >= opts.Rounds {
				return s.stop(ctx, result, OutOfRounds, fmt.Sprintf("%d implement rounds used", server.ChangesRequested(pr.Reviews)))
			}
			head := pr.HeadSHA
			if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue}); err != nil {
				return s.runFailure(ctx, result, err)
			}
			after, err := s.API.PR(ctx, opts.Repo, pr.Number)
			if err != nil {
				return result, err
			}
			if after.Merged {
				return s.stop(ctx, result, Merged, mergedDetail(after))
			}
			if after.HeadSHA == head {
				return s.stop(ctx, result, RunFailed, "the implement run ended without pushing a new head")
			}
			resuming = false
		case assessment.Decision == review.Commented:
			return s.stop(ctx, result, VerifierUndecided, fmt.Sprintf("%s left a comment on !%d", assessment.Review.Reviewer, pr.Number))
		default:
			head := pr.HeadSHA
			verify := RunRequest{Kind: policy.Verify, Repo: opts.Repo, Actor: opts.Verify, PR: pr.Number, SHA: head}
			if s.Checker != nil {
				outcome, err := s.gate(ctx, opts, pr)
				if err != nil {
					return s.runFailure(ctx, result, err)
				}
				switch {
				case outcome.Skipped:
				case outcome.Passed:
					verify.ChecksPassed = true
					verify.Note = passedNote(head, outcome)
				default:
					if fixing == head {
						return s.stop(ctx, result, RunFailed, fmt.Sprintf("the implement round to fix the failed checks at %s pushed no new commit", shortSHA(head)))
					}
					failed, err := s.API.GateFailures(ctx, opts.Repo, pr.Number)
					if err != nil {
						return result, err
					}
					if len(failed) > opts.CheckRounds {
						return s.stop(ctx, result, ChecksFailed, fmt.Sprintf("the post-implement checks failed at %s, and %d check rounds are used: %s; log: %s",
							shortSHA(head), opts.CheckRounds, outcome.Description, outcome.Log))
					}
					if err := s.API.Comment(ctx, opts.Repo, opts.Issue, roundBody(string(ChecksFailed), fmt.Sprintf("at %s on !%d; starting implement round %d of %d to fix them.\n\n%s\n\nLog: `%s`",
						shortSHA(head), pr.Number, len(failed), opts.CheckRounds, outcome.Description, outcome.Log))); err != nil {
						return result, err
					}
					fixing = head
					if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue, Note: failedNote(head, outcome)}); err != nil {
						return s.runFailure(ctx, result, err)
					}
					resuming = false
					continue
				}
			}
			if err := s.runStep(ctx, opts, verify); errors.Is(err, ErrHeadMoved) {
				// A push during the checks, or while waiting for the
				// verifier's slot: the new head is gated first.
				if s.Out != nil {
					fmt.Fprintf(s.Out, "flow #%d: !%d moved past %s before the verify run started\n", opts.Issue, pr.Number, shortSHA(head))
				}
				resuming = false
				continue
			} else if err != nil {
				return s.runFailure(ctx, result, err)
			}
			resuming = false
			after, err := s.waitReview(ctx, opts, pr.Number, head)
			if err != nil {
				return result, err
			}
			if after.Merged {
				return s.stop(ctx, result, Merged, mergedDetail(after))
			}
			if after.HeadSHA == head && after.Assessment.Decision == review.NeedsReview && autoDismissible(after) {
				// Forgejo dismisses a verdict with the older reviews when it
				// processes the push to head after the verdict.
				if err := s.API.ReconcileCarries(ctx, opts.Repo, after.Number); err != nil {
					return result, err
				}
				if after, err = s.API.PR(ctx, opts.Repo, after.Number); err != nil {
					return result, err
				}
			}
			if after.HeadSHA == head && after.Assessment.Decision == review.NeedsReview {
				return s.stop(ctx, result, VerifierUndecided, "the verify run ended without a verdict")
			}
		}
	}
}

// gate is the post-implement checks at pr's head: the flow's recorded
// status there, else a new check, which it records. The status, not this
// process, is the flow's memory, so a resumed flow does not check a head
// twice.
func (s *Supervisor) gate(ctx context.Context, opts Options, pr server.PROut) (CheckOutcome, error) {
	req := CheckRequest{Repo: opts.Repo, Issue: opts.Issue, PR: pr.Number, SHA: pr.HeadSHA}
	if st, ok := gateStatus(pr); ok {
		return CheckOutcome{Passed: st.State == "success", Description: st.Description, Log: s.Checker.LogPath(req)}, nil
	}
	if s.Out != nil {
		fmt.Fprintf(s.Out, "flow #%d: post-implement checks at %s\n", opts.Issue, shortSHA(pr.HeadSHA))
	}
	outcome, err := RunCheck(ctx, s.Checker, req, opts.CheckTimeout)
	if err != nil {
		return outcome, err
	}
	if outcome.Skipped {
		return outcome, nil
	}
	state := "failure"
	if outcome.Passed {
		state = "success"
	}
	if err := s.API.RecordGate(ctx, opts.Repo, pr.HeadSHA, state, outcome.Description); err != nil {
		return outcome, fmt.Errorf("recording the post-implement checks at %s: %w", shortSHA(pr.HeadSHA), err)
	}
	if s.Out != nil {
		fmt.Fprintf(s.Out, "flow #%d: post-implement checks %s at %s; log: %s\n", opts.Issue, state, shortSHA(pr.HeadSHA), outcome.Log)
	}
	return outcome, nil
}

// RunCheck runs c's checks at req's head within timeout. Running out of
// time is a failed check, not an error; an error is checks that could not
// run, or ctx ending.
func RunCheck(ctx context.Context, c Checker, req CheckRequest, timeout time.Duration) (CheckOutcome, error) {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	outcome, err := c.Check(checkCtx, req)
	timedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
	cancel()
	if ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	if err != nil && !timedOut {
		return outcome, fmt.Errorf("the post-implement checks could not run at %s: %w", shortSHA(req.SHA), err)
	}
	if timedOut {
		outcome.Passed, outcome.Skipped = false, false
		outcome.Description = fmt.Sprintf("timed out after %s; %s", timeout, outcome.Description)
		outcome.Summary = outcome.Description + "\n" + outcome.Summary
	}
	return outcome, nil
}

// gateStatus is the flow's own bottown/e2e status at the PR's head.
func gateStatus(pr server.PROut) (server.CommitStatus, bool) {
	for _, st := range pr.Checks {
		if st.Gate && st.Context == "bottown/e2e" {
			return st, true
		}
	}
	return server.CommitStatus{}, false
}

func passedNote(head string, o CheckOutcome) string {
	return fmt.Sprintf("bottown flow ran the post-implement checks (the repository's e2e commands) at `%s` itself, in a clean worktree, and they passed: %s\n\nLog: `%s`",
		head, o.Description, o.Log)
}

func failedNote(head string, o CheckOutcome) string {
	summary := strings.TrimSpace(o.Summary)
	if summary == "" {
		summary = o.Description
	}
	return fmt.Sprintf("This is an implement round to fix failed checks. bottown flow ran the post-implement checks (the repository's e2e commands) at `%s`, the pull request's head, in a clean worktree, and they failed:\n\n```\n%s\n```\n\nThe full log is `%s`: read it at the lines above. Fix the failure, push, and comment on the pull request with what changed; the flow runs the checks again at your new head before a verifier sees it.",
		head, summary, o.Log)
}

func conflictNote(pr server.PROut, conflicts []string) string {
	files := "files git did not name"
	if len(conflicts) > 0 {
		files = "`" + strings.Join(conflicts, "`, `") + "`"
	}
	return fmt.Sprintf("This is an implement round to resolve merge conflicts. `%s` has moved since pull request !%d branched from it, and merging it into `%s` at `%s` conflicts in %s, so bottown flow cannot merge it for you.\n\nFetch the forge remote and merge its `%s` into your branch (`git fetch bottown && git merge bottown/%s`), resolve the conflicts keeping the intent of both sides, run the repository's checks, push, and comment on the pull request with how you resolved them. Merge; do not rebase or force-push. The flow then checks and verifies your new head.",
		pr.BaseBranch, pr.Number, pr.HeadBranch, pr.HeadSHA, files, pr.BaseBranch, pr.BaseBranch)
}

// waitForPush lets Forgejo finish updating mergeability and reviews after a
// head change. A verdict submitted before the push task settles the reviews
// can be dismissed as stale even when it names the new head.
func (s *Supervisor) waitForPush(ctx context.Context, opts Options, pr server.PROut) (server.PROut, error) {
	for range 50 {
		if pr.Mergeable && !pr.Syncing && reviewsSettled(pr) {
			return pr, nil
		}
		if err := waitForge(ctx); err != nil {
			return pr, err
		}
		var err error
		pr, err = s.API.PR(ctx, opts.Repo, pr.Number)
		if err != nil {
			return pr, err
		}
	}
	if !reviewsSettled(pr) {
		return pr, fmt.Errorf("pull request !%d reviews did not settle at %s", pr.Number, shortSHA(pr.HeadSHA))
	}
	return pr, nil
}

func reviewsSettled(pr server.PROut) bool {
	for _, r := range pr.Reviews {
		if r.Dismissed || r.CommitID == "" {
			continue
		}
		// Forgejo leaves an approval at an older head unstale when the push
		// left the PR's diff alone; one that carries over is settled
		// either way, whichever approval decides the assessment.
		if carried(pr, r.CommitID) {
			continue
		}
		if wantStale := r.CommitID != pr.HeadSHA; r.Stale != wantStale {
			return false
		}
	}
	return true
}

// autoDismissible reports whether pr has a dismissed official approval the
// reconcile step may undismiss: one that carries over to the head, or one at
// the head itself.
func autoDismissible(pr server.PROut) bool {
	for _, r := range pr.Reviews {
		if r.Dismissed && r.Official && r.State == review.StateApproved && (r.CommitID == pr.HeadSHA || carried(pr, r.CommitID)) {
			return true
		}
	}
	return false
}

// carried reports whether reviews at commit carry over to pr's head.
func carried(pr server.PROut, commit string) bool {
	for _, c := range pr.Carries {
		if c.From == commit {
			return true
		}
	}
	return false
}

func (s *Supervisor) waitReview(ctx context.Context, opts Options, number int64, head string) (server.PROut, error) {
	for range 50 {
		pr, err := s.API.PR(ctx, opts.Repo, number)
		if err != nil {
			return pr, err
		}
		if pr.Merged || pr.HeadSHA != head || pr.Assessment.Decision != review.NeedsReview || autoDismissible(pr) {
			return pr, nil
		}
		if err := waitForge(ctx); err != nil {
			return pr, err
		}
	}
	return s.API.PR(ctx, opts.Repo, number)
}

func waitForge(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func (s *Supervisor) checkFamilies(ctx context.Context, opts Options) error {
	actors, err := s.API.Actors(ctx)
	if err != nil {
		return err
	}
	families := map[string]string{}
	for _, actor := range actors {
		families[actor.Name] = actor.Family
	}
	implActor, _, err := runs.SplitActorSpec(opts.Implement)
	if err != nil {
		return fmt.Errorf("implementer: %w", err)
	}
	verActor, _, err := runs.SplitActorSpec(opts.Verify)
	if err != nil {
		return fmt.Errorf("verifier: %w", err)
	}
	implement, ok := families[implActor]
	if !ok {
		return fmt.Errorf("unknown implementer %q", implActor)
	}
	verify, ok := families[verActor]
	if !ok {
		return fmt.Errorf("unknown verifier %q", verActor)
	}
	if implement == verify {
		return fmt.Errorf("implementer %s and verifier %s are both %s; cross-family verification is required", implActor, verActor, implement)
	}
	return nil
}

func (s *Supervisor) checkLiveRuns(ctx context.Context, opts Options, pr int64) error {
	runs, err := s.API.LiveRuns(ctx, opts.Repo)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, run := range runs {
		if run.Active(now) && (run.Issue == opts.Issue || pr > 0 && run.PR == pr) {
			return fmt.Errorf("run %s is still live for this issue; end it before resuming the flow: "+
				"`bottown run stop %s` stops it, or, if no launcher in this checkout holds it, "+
				"`bottown run stop --revoke %s` revokes it", run.ID, run.ID, run.ID)
		}
	}
	return nil
}

func (s *Supervisor) runStep(ctx context.Context, opts Options, req RunRequest) error {
	delay := s.RetryDelay
	if delay == 0 {
		delay = time.Second
	}
	req.SerializeActor = opts.SerializeActors
	req.Round, req.Rounds = opts.round, opts.Rounds
	now, after := s.now, s.after
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	var deadline time.Time // of the wait for a busy actor, once one starts
	for {
		// The owner may merge the PR while a run, or the wait for a busy
		// actor, goes on: then no run is left to start.
		if detail, ok, err := s.landed(ctx, opts); err != nil {
			return err
		} else if ok {
			return &landedError{detail: detail}
		}
		if s.Out != nil && !req.Retry {
			target := fmt.Sprintf("#%d", req.Issue)
			if req.PR > 0 {
				target = fmt.Sprintf("!%d", req.PR)
			}
			fmt.Fprintf(s.Out, "flow #%d: %s %s with %s\n", opts.Issue, req.Kind, target, req.Actor)
		}
		runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		err := s.Runner.Run(runCtx, req)
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			return nil
		}
		if opts.SerializeActors && errors.Is(err, ErrActorBusy) {
			now := now()
			if deadline.IsZero() {
				deadline = now.Add(opts.ActorWait)
				if s.Out != nil {
					fmt.Fprintf(s.Out, "flow #%d: waiting up to %s for %s's other batch run to end: %v\n", opts.Issue, opts.ActorWait, req.Actor, err)
				}
			}
			if !now.Before(deadline) {
				return &actorWaitError{actor: req.Actor, wait: opts.ActorWait, err: err}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-after(min(delay, deadline.Sub(now))):
			}
			delay = min(2*delay, max(maxRetryDelay, s.RetryDelay))
			req.Retry = true
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if timedOut {
			return fmt.Errorf("%s run timed out after %s", req.Kind, opts.Timeout)
		}
		var failed *runs.FailedRunError
		if errors.As(err, &failed) && failed.Log != "" && !failed.ToolCalls {
			return &idleRunError{kind: req.Kind, actor: req.Actor, err: failed}
		}
		return err
	}
}

// idleRunError is a batch run whose agent made no tool call, for the
// AgentDidNothing stop; actor is ACTOR[:MODEL], as the flow ran it.
type idleRunError struct {
	kind, actor string
	err         *runs.FailedRunError
}

func (e *idleRunError) Error() string {
	actor, model, _ := strings.Cut(e.actor, ":")
	if model != "" {
		actor += " (model " + model + ")"
	}
	return fmt.Sprintf("the %s run %s by %s %s without a tool call", e.kind, e.err.ID, actor, e.err.Status)
}

func (e *idleRunError) Unwrap() error { return e.err }

// actorWaitError is a run whose actor was still busy at the end of
// Options.ActorWait; err is the last refusal, which names the run that
// holds the actor.
type actorWaitError struct {
	actor string
	wait  time.Duration
	err   error
}

func (e *actorWaitError) Error() string {
	return fmt.Sprintf("%s still has another live batch run after %s: %v", e.actor, e.wait, e.err)
}

func (e *actorWaitError) Unwrap() error { return e.err }

// landedError is a run step not started because the work has landed;
// detail says how, for the Merged stop.
type landedError struct{ detail string }

func (e *landedError) Error() string { return e.detail }

// landed reports whether the work has landed, so no run is left to start:
// the flow's PR is merged, or the issue is closed. detail says which.
func (s *Supervisor) landed(ctx context.Context, opts Options) (detail string, ok bool, err error) {
	if opts.pr > 0 {
		pr, err := s.API.PR(ctx, opts.Repo, opts.pr)
		if err != nil {
			return "", false, err
		}
		if pr.Merged {
			return mergedDetail(pr), true, nil
		}
	}
	is, err := s.API.Issue(ctx, opts.Repo, opts.Issue)
	if err != nil {
		return "", false, err
	}
	if is.State != "open" {
		return fmt.Sprintf("issue #%d is %s; nothing left to do", opts.Issue, is.State), true, nil
	}
	return "", false, nil
}

func mergedDetail(pr server.PROut) string {
	return fmt.Sprintf("!%d was merged at %s; nothing left to do", pr.Number, shortSHA(pr.HeadSHA))
}

func (s *Supervisor) runFailure(ctx context.Context, result Result, err error) (Result, error) {
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var landed *landedError
	if errors.As(err, &landed) {
		return s.stop(ctx, result, Merged, landed.detail)
	}
	if errors.Is(err, ErrIssueClosed) {
		// Closed between landed's read and the run's creation.
		detail := fmt.Sprintf("issue #%d was closed before the run started; nothing left to do", result.Issue)
		if result.PR > 0 {
			if pr, perr := s.API.PR(ctx, result.Repo, result.PR); perr == nil && pr.Merged {
				detail = mergedDetail(pr)
			}
		}
		return s.stop(ctx, result, Merged, detail)
	}
	var busy *actorWaitError
	if errors.As(err, &busy) {
		return s.stop(ctx, result, ActorBusy, busy.Error()+"; end that run, or re-run the flow when it has ended")
	}
	var idle *idleRunError
	if errors.As(err, &idle) {
		detail := idle.Error() + "; log: " + idle.err.Log
		if idle.err.LastError != "" {
			detail += "; last error: " + idle.err.LastError
		}
		detail += "; if the actor is unavailable (quota, sign-in), rerun the flow with another `--implement`/`--verify`"
		result.Actor, _, _ = strings.Cut(idle.actor, ":")
		return s.stop(ctx, result, AgentDidNothing, detail)
	}
	return s.stop(ctx, result, RunFailed, runFailedDetail(err))
}

// runFailedDetail is a RunFailed stop's detail: for a batch run, also its
// log and the harness's last error line.
func runFailedDetail(err error) string {
	detail := err.Error()
	var failed *runs.FailedRunError
	if !errors.As(err, &failed) || failed.Log == "" {
		return detail
	}
	detail += "; log: " + failed.Log
	if failed.LastError != "" {
		detail += "; last error: " + failed.LastError
	}
	return detail
}

func (s *Supervisor) stop(ctx context.Context, result Result, reason StopReason, detail string) (Result, error) {
	result.Reason, result.Detail = reason, detail
	if err := s.API.Comment(ctx, result.Repo, result.Issue, stopBody(reason, detail, result.PR, result.Actor)); err != nil {
		return result, fmt.Errorf("flow stopped: %s, but posting the stop comment failed: %w", reason, err)
	}
	if reason == ReadyToMerge {
		return result, nil
	}
	return result, &StopError{Result: result}
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return strings.TrimSpace(sha)
}
