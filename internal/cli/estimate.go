package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/agents"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/estimate"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// bottown estimate (#439, docs/reference/sizing.md): one estimate run per
// issue, each sizing its issue by the sizing guide with issue_size.

type estimateOpts struct {
	actors  string
	max     int
	timeout time.Duration
	repo    string
	again   bool
	keep    bool
}

// defaultEstimateMax is how many unsized issues estimate takes without
// issue arguments: a first run over a large backlog is a choice, not a
// default.
const defaultEstimateMax = 10

func newEstimate(g *globals) *cobra.Command {
	var o estimateOpts
	c := &cobra.Command{
		Use:   "estimate [ISSUE...] --actors SPEC [--max N] [--again] [--timeout D] [--repo R]",
		Short: "Size issues by the sizing guide: one estimate run per issue",
		Long: `Size issues by the sizing guide, one estimate run per issue. Each run reads
its issue, its discussion and the code at the default branch's head, and
records its estimate with issue_size: a comment on the issue, and the size
label (size/S … size/XL) when the issue has none, or needs/clarification
with questions for you. The runs cannot push, open pull requests or review.

Without ISSUE arguments, estimate sizes the open issues that have no size/*
and no needs/clarification label, leaving out milestone epics, oldest
first, at most --max of them. Named issues that are already sized are
skipped, saying so; with --again they are estimated all the same, and the
estimate is posted as a second opinion without changing the label.

--actors is the pool, comma-separated ACTOR[:MODEL][=N] entries: N is how
many runs the actor has at once, a number or * (default 1). Each issue goes
to the next actor with a free slot, so a pool of several actors spreads the
issues among them. One family is enough: estimates are not verified.

When the runs end, estimate prints each issue's outcome, size and deciding
factor. A run that ends without recording an estimate has failed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEstimate(cmd, g, args, o)
		},
	}
	c.Flags().StringVar(&o.actors, "actors", "", "the pool: comma-separated ACTOR[:MODEL][=N] entries, N a number or * (default 1) (required)")
	c.Flags().IntVar(&o.max, "max", defaultEstimateMax, "without ISSUE arguments, the most unsized issues to estimate")
	c.Flags().BoolVar(&o.again, "again", false, "estimate named issues that are already sized, as a second opinion that keeps their label")
	c.Flags().DurationVar(&o.timeout, "timeout", 15*time.Minute, "the time limit for each run")
	c.Flags().StringVar(&o.repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	c.Flags().BoolVar(&o.keep, "keep", false, "keep the worktrees even if the runs succeed")
	_ = c.MarkFlagRequired("actors")
	return c
}

// estimatePool parses --actors: agents.ParseEntries' grammar, where an
// entry without =N has one run at a time.
func estimatePool(spec string, actors []config.Actor) ([]agents.Entry, error) {
	var parts []string
	for _, p := range strings.Split(spec, ",") {
		if p != "" && !strings.Contains(p, "=") {
			p += "=1"
		}
		parts = append(parts, p)
	}
	entries, err := agents.ParseEntries(strings.Join(parts, ","), actors)
	if err != nil {
		return nil, fmt.Errorf("--actors: %w", err)
	}
	return entries, nil
}

// unsizedIssues are the issues estimate takes without arguments: open
// issues with no size and no needs/clarification label, not pull requests
// nor milestone epics, oldest first, at most limit.
func unsizedIssues(items []server.IssueItem, limit int) []int64 {
	var picked []server.IssueItem
	for _, it := range items {
		if it.PullRequest || it.State != "open" || estimate.SizeLabel(it.Labels) != "" || slices.Contains(it.Labels, estimate.ClarificationLabel) {
			continue
		}
		if m, _, ok := milestone.ParseMarker(it.Body); ok && m.Epic {
			continue
		}
		picked = append(picked, it)
	}
	slices.SortFunc(picked, func(a, b server.IssueItem) int {
		return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.Number, b.Number))
	})
	var ns []int64
	for _, it := range picked {
		if len(ns) == limit {
			break
		}
		ns = append(ns, it.Number)
	}
	return ns
}

// namedIssues splits the issues named on the command line into those to
// estimate and those skipped, with why: already sized, unless again.
func namedIssues(issues []server.IssueOut, again bool) (todo []int64, skipped []string) {
	for _, is := range issues {
		if l := estimate.SizeLabel(is.Labels); l != "" && !again {
			skipped = append(skipped, fmt.Sprintf("#%d is already %s; skipped (--again estimates it all the same)", is.Number, l))
			continue
		}
		todo = append(todo, is.Number)
	}
	return todo, skipped
}

// slots hands out the pool's actors to runs: each issue goes to the next
// entry, in spec order from the last one used, that has a free slot.
type slots struct {
	mu      sync.Mutex
	free    *sync.Cond
	entries []agents.Entry
	live    map[string]int
	next    int
}

func newSlots(entries []agents.Entry) *slots {
	s := &slots{entries: entries, live: map[string]int{}}
	s.free = sync.NewCond(&s.mu)
	return s
}

// take waits for an entry with a free slot and takes it.
func (s *slots) take() agents.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		for i := range len(s.entries) {
			j := (s.next + i) % len(s.entries)
			e := s.entries[j]
			if e.Limit == 0 || s.live[e.Actor] < e.Limit {
				s.live[e.Actor]++
				s.next = (j + 1) % len(s.entries)
				return e
			}
		}
		s.free.Wait()
	}
}

// give returns e's slot.
func (s *slots) give(e agents.Entry) {
	s.mu.Lock()
	s.live[e.Actor]--
	s.mu.Unlock()
	s.free.Broadcast()
}

// estimateResult is one issue's estimate, as the command reports it.
type estimateResult struct {
	Issue   int64
	Actor   string
	Run     string
	Outcome string // secrets.Outcome*, or "" when the run recorded none
	Size    string
	Factor  string
	Label   string // the label the issue kept, when the estimate was not applied
	Err     error
}

func runEstimate(cmd *cobra.Command, g *globals, args []string, o estimateOpts) error {
	if o.max < 1 {
		return errors.New("--max must be at least 1")
	}
	var named []int64
	for _, a := range args {
		n, err := parseNumber(a)
		if err != nil {
			return err
		}
		if !slices.Contains(named, n) {
			named = append(named, n)
		}
	}
	if o.again && len(named) == 0 {
		return errors.New("--again goes with named issues: without them, estimate takes only unsized issues")
	}
	ctx, out, errOut := cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	rc, err := openRunContextRepo(g, o.repo)
	if err != nil {
		return err
	}
	var infos []server.ActorInfo
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/actors", nil, &infos); err != nil {
		return err
	}
	actors := make([]config.Actor, len(infos))
	for i, a := range infos {
		actors[i] = config.Actor{Name: a.Name, Family: a.Family, Harness: a.Harness}
	}
	entries, err := estimatePool(o.actors, actors)
	if err != nil {
		return err
	}

	var todo []int64
	if len(named) > 0 {
		var issues []server.IssueOut
		for _, n := range named {
			q := repoQuery(rc.repo)
			q.Set("number", strconv.FormatInt(n, 10))
			q.Set("comments", "0")
			var is server.IssueOut
			if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/forge/issue?"+q.Encode(), nil, &is); err != nil {
				return fmt.Errorf("#%d: %w", n, err)
			}
			issues = append(issues, is)
		}
		var skipped []string
		todo, skipped = namedIssues(issues, o.again)
		for _, s := range skipped {
			fmt.Fprintf(out, "estimate: %s\n", s)
		}
	} else {
		q := repoQuery(rc.repo)
		q.Set("state", "open")
		q.Set("body", "1")
		var items []server.IssueItem
		if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/forge/issues?"+q.Encode(), nil, &items); err != nil {
			return err
		}
		todo = unsizedIssues(items, o.max)
	}
	if len(todo) == 0 {
		fmt.Fprintln(out, "estimate: no issues to estimate")
		return nil
	}
	fmt.Fprintf(out, "estimate: %d issue(s): %s\n", len(todo), issueList(todo))

	// The runs start from one fetch of the mirror.
	if err := rc.ws.Sync(ctx); err != nil {
		return err
	}
	pool := newSlots(entries)
	results := make([]estimateResult, len(todo))
	var wg sync.WaitGroup
	for i, n := range todo {
		e := pool.take()
		results[i] = estimateResult{Issue: n, Actor: e.String()}
		wg.Go(func() {
			defer pool.give(e)
			rctx, cancel := context.WithTimeout(ctx, o.timeout)
			defer cancel()
			ro := runOpts{actor: e.String(), repo: o.repo, keep: o.keep, plain: true, synced: true, ctx: rctx, runID: &results[i].Run}
			results[i].Err = startRun(cmd, g, policy.Estimate, strconv.FormatInt(n, 10), ro)
		})
	}
	wg.Wait()

	failed := 0
	for i := range results {
		r := &results[i]
		if r.Run != "" {
			readEstimate(ctx, rc, r)
		}
		if r.Err != nil || r.Outcome == "" {
			failed++
		}
	}
	printEstimates(out, results)
	if failed > 0 {
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(errOut, "estimate: #%d: %v\n", r.Issue, r.Err)
			}
		}
		return fmt.Errorf("estimate: %d of %d run(s) recorded no estimate", failed, len(results))
	}
	return nil
}

func issueList(ns []int64) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(s, ", ")
}

// readEstimate fills r from the estimate its run posted on its issue, if
// any: the newest comment whose record names the run.
func readEstimate(ctx context.Context, rc *runContext, r *estimateResult) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	q := repoQuery(rc.repo)
	q.Set("number", strconv.FormatInt(r.Issue, 10))
	var is server.IssueOut
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/forge/issue?"+q.Encode(), nil, &is); err != nil {
		if r.Err == nil {
			r.Err = fmt.Errorf("reading the estimate: %w", err)
		}
		return
	}
	estimateFromComments(r, is.Comments, estimate.SizeLabel(is.Labels))
}

// estimateFromComments fills r from the newest of comments that is r's
// run's estimate; label is the issue's size label now.
func estimateFromComments(r *estimateResult, comments []server.CommentOut, label string) {
	for i := len(comments) - 1; i >= 0; i-- {
		rec, ok := estimate.Parse(comments[i].Body)
		if !ok || rec.Run != r.Run {
			continue
		}
		r.Size, r.Factor = rec.Size, estimate.DecidingFactor(comments[i].Body)
		switch {
		case !rec.Applied:
			r.Outcome, r.Label = secrets.OutcomeAlreadySized, cmp.Or(rec.Existing, label)
		case rec.Size == estimate.NeedsClarification:
			r.Outcome = secrets.OutcomeNeedsClarification
		default:
			r.Outcome = secrets.OutcomeSized
		}
		return
	}
}

// printEstimates prints one line per issue: its outcome, size and
// deciding factor, or why it has none.
func printEstimates(w io.Writer, results []estimateResult) {
	st := newStyles()
	tw := newTableWriter(colorOut(w))
	tw.Row(st.Header.Render("ISSUE"), st.Header.Render("ACTOR"), st.Header.Render("RUN"), st.Header.Render("OUTCOME"), st.Header.Render("SIZE"), st.Header.Render("DECIDING FACTOR"))
	for _, r := range results {
		outcome, size, factor := r.Outcome, r.Size, r.Factor
		switch {
		case outcome == "":
			outcome, size, factor = "failed", "-", "no estimate recorded"
		case outcome == secrets.OutcomeAlreadySized:
			factor = fmt.Sprintf("kept %s; %s", r.Label, orDash(factor))
		case outcome == secrets.OutcomeNeedsClarification:
			size, factor = "-", "questions posted on the issue"
		}
		tw.Row(fmt.Sprintf("#%d", r.Issue), r.Actor, orDash(r.Run), outcome, size, orDash(factor))
	}
	_ = tw.Flush()
}

// estimateFailure is why an estimate run that exited cleanly did not do
// its one job: the server recorded no estimate for it (its outcome), nil
// when it did or the run is not an estimate run.
func estimateFailure(run, onServer secrets.Run) *secrets.Failure {
	if run.Kind != policy.Estimate {
		return nil
	}
	switch onServer.Outcome {
	case secrets.OutcomeSized, secrets.OutcomeAlreadySized, secrets.OutcomeNeedsClarification:
		return nil
	}
	return &secrets.Failure{Cause: secrets.FailureNoOutput, Detail: "the run recorded no estimate (bottown issue size)"}
}

// checkEstimated is estimateFailure against the server's record of run,
// before it ends. When the record cannot be read, the run's outcome
// stands, with a warning.
func checkEstimated(ctx context.Context, rc *runContext, run secrets.Run, errOut io.Writer) *secrets.Failure {
	if run.Kind != policy.Estimate {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var current secrets.Run
	if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs/"+url.PathEscape(run.ID), nil, &current); err != nil {
		fmt.Fprintf(errOut, "warning: read the estimate run's outcome: %v\n", err)
		return nil
	}
	return estimateFailure(run, current)
}
