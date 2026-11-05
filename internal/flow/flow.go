// Package flow supervises one issue through implement and cross-family
// verification, rebuilding its position from the forge and run records.
package flow

import (
	"bufio"
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
	ReadyToMerge      StopReason = "ready to merge"
	OutOfRounds       StopReason = "out of rounds"
	RunFailed         StopReason = "run failed"
	VerifierUndecided StopReason = "verifier undecided"
	ConflictsWithMain StopReason = "conflicts with main"
	// ChangesRequested: the head is approved, but a request for changes at
	// an older head still blocks the merge.
	ChangesRequested StopReason = "changes requested"
	// ChecksFailed: the post-implement checks failed at the head and no
	// check rounds remain.
	ChecksFailed StopReason = "checks failed"
)

// ErrActorLimit marks a run creation refused by the per-actor batch limit.
var ErrActorLimit = errors.New("actor at live batch run limit")

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
	WaitActors   bool
	// Attended flows ask on In before merging a clean, behind base; the
	// others leave that to the owner inbox.
	Attended bool
}

// Result is the flow's final position.
type Result struct {
	Issue  int64
	Repo   string
	PR     int64
	Reason StopReason
	Detail string
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
	Kind             string
	Repo             string
	Actor            string // ACTOR[:SPEC], as given in Options
	Issue            int64
	PR               int64
	BypassActorLimit bool
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
	OpenPR(context.Context, string, string) (server.PRItem, bool, error)
	PR(context.Context, string, int64) (server.PROut, error)
	LiveRuns(context.Context, string) ([]secrets.Run, error)
	Comment(context.Context, string, int64, string) error
	// RecordGate writes the post-implement checks' result at sha as the
	// flow's bottown/e2e commit status.
	RecordGate(ctx context.Context, repo, sha, state, description string) error
	// GateFailures lists the PR's commits where they failed.
	GateFailures(ctx context.Context, repo string, pr int64) ([]string, error)
	// PRMergeStatus trial-merges the PR's base into its head; MergeBase
	// pushes that merge when the head is still the one given.
	PRMergeStatus(context.Context, string, int64) (server.PRMergeStatus, error)
	MergeBase(context.Context, string, int64, string) (server.PRMergeOut, error)
}

// Supervisor drives one issue. RetryDelay is how long to wait before trying
// an actor-limited run again; zero uses a short default suitable for tests.
// Checker, if set, gates each new head on the post-implement checks before
// a verifier sees it.
type Supervisor struct {
	API        API
	Runner     Runner
	Checker    Checker
	In         io.Reader // the owner's answers, for an attended flow
	Out        io.Writer
	RetryDelay time.Duration

	answers *bufio.Reader
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
	if s.Checker != nil && (opts.CheckRounds < 0 || opts.CheckTimeout <= 0) {
		return result, errors.New("check rounds must not be negative, and the check timeout must be positive")
	}
	if s.API == nil || s.Runner == nil {
		return result, errors.New("flow API and runner are required")
	}
	if err := s.checkFamilies(ctx, opts); err != nil {
		return result, err
	}
	resuming := true
	// fixing is the head whose failed checks the last implement round was
	// asked to fix: if it pushed nothing, the flow stops.
	fixing := ""

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
				return s.stop(ctx, result, RunFailed, "the implement run ended without opening a PR")
			}
			continue
		}

		pr, err = s.waitForPush(ctx, opts, pr)
		if err != nil {
			return result, err
		}
		merge, err := s.API.PRMergeStatus(ctx, opts.Repo, pr.Number)
		if err != nil {
			return result, err
		}
		if merge.Behind && !merge.Clean {
			files := strings.Join(merge.Conflicts, ", ")
			if files == "" {
				files = "unknown files"
			}
			return s.stop(ctx, result, ConflictsWithMain, fmt.Sprintf("merging %s into %s for !%d would conflict in %s; start an implement run to resolve it", pr.BaseBranch, pr.HeadBranch, pr.Number, files))
		}
		if merge.Behind && merge.Clean && opts.Attended {
			yes, err := s.confirmMerge(pr)
			if err != nil {
				return result, err
			}
			if yes {
				merged, err := s.API.MergeBase(ctx, opts.Repo, pr.Number, merge.HeadSHA)
				if err != nil {
					return result, err
				}
				if s.Out != nil {
					fmt.Fprintf(s.Out, "flow #%d: merged %s into %s for !%d at %s\n", opts.Issue, pr.BaseBranch, pr.HeadBranch, pr.Number, shortSHA(merged.HeadSHA))
				}
				continue
			}
		}
		if !pr.Mergeable && !(merge.Behind && merge.Clean) {
			if !resuming {
				return s.stop(ctx, result, ConflictsWithMain, fmt.Sprintf("!%d no longer merges cleanly; rerun the flow to resolve it", pr.Number))
			}
			if countRounds(pr.Reviews) >= opts.Rounds {
				return s.stop(ctx, result, OutOfRounds, "the PR conflicts with main and no implement rounds remain")
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
			return s.stop(ctx, result, ReadyToMerge, detail)
		case assessment.Decision == review.ChangesRequested && !assessment.AtHead(pr.HeadSHA):
			// A follow-up cannot clear an older head's request: only its
			// reviewer, reviewing again, or the owner, dismissing it.
			blocking := assessment.Review
			return s.stop(ctx, result, ChangesRequested, fmt.Sprintf("!%d is approved at %s, but %s's request for changes at %s still blocks it; %s must review again, or the owner dismisses it",
				pr.Number, shortSHA(pr.HeadSHA), blocking.Reviewer, shortSHA(blocking.CommitID), blocking.Reviewer))
		case assessment.Decision == review.ChangesRequested:
			if countRounds(pr.Reviews) >= opts.Rounds {
				return s.stop(ctx, result, OutOfRounds, fmt.Sprintf("%d implement rounds used", countRounds(pr.Reviews)))
			}
			head := pr.HeadSHA
			if err := s.runStep(ctx, opts, RunRequest{Kind: policy.Implement, Repo: opts.Repo, Actor: opts.Implement, Issue: opts.Issue}); err != nil {
				return s.runFailure(ctx, result, err)
			}
			after, err := s.API.PR(ctx, opts.Repo, pr.Number)
			if err != nil {
				return result, err
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
					if err := s.API.Comment(ctx, opts.Repo, opts.Issue, fmt.Sprintf("**bottown flow: checks failed** at %s on !%d; starting implement round %d of %d to fix them.\n\n%s\n\nLog: `%s`",
						shortSHA(head), pr.Number, len(failed), opts.CheckRounds, outcome.Description, outcome.Log)); err != nil {
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
			if after.HeadSHA == head {
				if after.Assessment.Decision == review.NeedsReview {
					return s.stop(ctx, result, VerifierUndecided, "the verify run ended without a verdict")
				}
			}
		}
	}
}

// confirmMerge asks the owner whether to merge the base into pr's branch.
// Anything but y or yes, end of input included, is no.
func (s *Supervisor) confirmMerge(pr server.PROut) (bool, error) {
	if s.In == nil {
		return false, nil
	}
	if s.Out != nil {
		fmt.Fprintf(s.Out, "Merge %s into %s for !%d? [y/N] ", pr.BaseBranch, pr.HeadBranch, pr.Number)
	}
	if s.answers == nil {
		s.answers = bufio.NewReader(s.In)
	}
	answer, err := s.answers.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
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
	checkCtx, cancel := context.WithTimeout(ctx, opts.CheckTimeout)
	outcome, err := s.Checker.Check(checkCtx, req)
	timedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
	cancel()
	if ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	if err != nil && !timedOut {
		return outcome, fmt.Errorf("the post-implement checks could not run at %s: %w", shortSHA(pr.HeadSHA), err)
	}
	if timedOut {
		outcome.Passed, outcome.Skipped = false, false
		outcome.Description = fmt.Sprintf("timed out after %s; %s", opts.CheckTimeout, outcome.Description)
		outcome.Summary = outcome.Description + "\n" + outcome.Summary
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

// waitForPush lets Forgejo finish updating mergeability and reviews after a
// head change. A verdict submitted before the push task settles the reviews
// can be dismissed as stale even when it names the new head.
func (s *Supervisor) waitForPush(ctx context.Context, opts Options, pr server.PROut) (server.PROut, error) {
	for range 50 {
		if pr.Mergeable && reviewsSettled(pr) {
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
		if pr.HeadSHA != head || pr.Assessment.Decision != review.NeedsReview {
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
	req.BypassActorLimit = !opts.WaitActors
	for {
		if s.Out != nil {
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
		if opts.WaitActors && errors.Is(err, ErrActorLimit) {
			if s.Out != nil {
				fmt.Fprintf(s.Out, "flow #%d: waiting for %s's actor slot\n", opts.Issue, req.Actor)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if timedOut {
			return fmt.Errorf("%s run timed out after %s", req.Kind, opts.Timeout)
		}
		return err
	}
}

func (s *Supervisor) runFailure(ctx context.Context, result Result, err error) (Result, error) {
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return s.stop(ctx, result, RunFailed, err.Error())
}

func (s *Supervisor) stop(ctx context.Context, result Result, reason StopReason, detail string) (Result, error) {
	result.Reason, result.Detail = reason, detail
	body := fmt.Sprintf("**bottown flow: %s**\n\n%s", reason, detail)
	if result.PR > 0 {
		body += fmt.Sprintf("\n\nPull request: !%d", result.PR)
	}
	if err := s.API.Comment(ctx, result.Repo, result.Issue, body); err != nil {
		return result, fmt.Errorf("flow stopped: %s, but posting the stop comment failed: %w", reason, err)
	}
	if reason == ReadyToMerge {
		return result, nil
	}
	return result, &StopError{Result: result}
}

// countRounds includes stale and dismissed reviews: each requested change
// consumed an implement round, even when it no longer gates the current head.
func countRounds(reviews []server.ReviewOut) int {
	count := 0
	for _, review := range reviews {
		if review.State == "REQUEST_CHANGES" {
			count++
		}
	}
	return count
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return strings.TrimSpace(sha)
}
