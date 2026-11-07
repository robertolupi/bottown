package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/robertolupi/bottown/internal/wiki"
)

// bottown debate (#315): independent rounds of discuss runs on one
// question, each answering in its own wiki file, merged by a consolidate
// run. The server places each run in its round and keeps it to its round's
// files (internal/server/debate.go).

type debateOpts struct {
	actors      []string
	consolidate string
	slug        string
	date        string
	raw         bool
	keep        bool
	question    string
}

func newDebate(g *globals) *cobra.Command {
	var o debateOpts
	var questionFile string
	c := &cobra.Command{
		Use:   "debate ISSUE [--actors A,B,...] [--consolidate ACTOR] [--wiki-slug S] [--date YYYYMMDD|today] [--raw] [QUESTION... | --question-file F|-]",
		Short: "Ask several agents the same question independently, and consolidate their answers in the wiki",
		Long: `Run a round of a debate on an issue: one discuss run per actor in --actors,
side by side, each answering the question in its own wiki file,
reviews/<debate>/r<N>-<actor>.md, without seeing the others' answers. While
the round is open its runs cannot comment on the issue or file issues, and
nobody but the owner reads its answers.

With --consolidate, once the round's runs have ended, a consolidate run by
that actor merges their answers into r<N>-consolidated.md; bottown then
rewrites the debate's page, Review-<debate>, from it, and the run's handover
comment links the page on the issue. --consolidate alone consolidates the
last round, for instance after a run of the round was retried.

The next round starts with another bottown debate, once the round before is
consolidated. Its runs read the earlier rounds' consolidations, and with
--raw their answers too. A round takes actors until it is consolidated: run
bottown debate again to add one, without a question or with the round's.

The debate is named [<date>-]<issue>[-<slug>]. The slug defaults to the
issue's one debate (under --date), else to the first five words of its
title; --wiki-slug names it, which starts another debate on the issue.
--date YYYYMMDD (or today) puts the debate under that day: continue an
earlier day's debate with its date.

Without a question, the agents review the issue.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := discussQuestion(cmd, args[1:], questionFile)
			if err != nil {
				return err
			}
			o.question = q
			return runDebate(cmd, g, args[0], o)
		},
	}
	c.Flags().StringSliceVar(&o.actors, "actors", nil, "the round's actors, comma-separated, each ACTOR or ACTOR:MODEL")
	c.Flags().StringVar(&o.consolidate, "consolidate", "", "consolidate the round once it ends, as ACTOR or ACTOR:MODEL")
	c.Flags().StringVar(&o.slug, "wiki-slug", "", "the debate's slug (default: the issue's debate, else from its title)")
	c.Flags().StringVar(&o.date, "date", "", "hold the debate under a date: YYYYMMDD or today")
	c.Flags().BoolVar(&o.raw, "raw", false, "a new round's runs also read earlier rounds' answers, not only their consolidations")
	c.Flags().BoolVar(&o.keep, "keep", false, "keep the worktrees even if the runs succeed")
	c.Flags().StringVar(&questionFile, "question-file", "", "read the question from a file, or - for stdin")
	return c
}

// debateDate is --date as the debate's name holds it: YYYY-MM-DD.
func debateDate(s string, now time.Time) (string, error) {
	switch s {
	case "":
		return "", nil
	case "today":
		return now.Format(time.DateOnly), nil
	}
	t, err := time.Parse("20060102", s)
	if err != nil {
		return "", fmt.Errorf("--date %q: give YYYYMMDD or today", s)
	}
	return t.Format(time.DateOnly), nil
}

// debateActors checks --actors: each actor once.
func debateActors(specs []string) error {
	var seen []string
	for _, s := range specs {
		a, _, err := runs.SplitActorSpec(s)
		if err != nil {
			return err
		}
		if slices.Contains(seen, a) {
			return fmt.Errorf("--actors names %s twice: a round has one answer per actor", a)
		}
		seen = append(seen, a)
	}
	return nil
}

func runDebate(cmd *cobra.Command, g *globals, target string, o debateOpts) error {
	if len(o.actors) == 0 && o.consolidate == "" {
		return errors.New("give the round's --actors, --consolidate, or both")
	}
	if len(o.actors) == 0 && o.question != "" {
		return errors.New("a question goes with --actors: a consolidate run merges the round's question")
	}
	if err := debateActors(o.actors); err != nil {
		return err
	}
	if err := wiki.ValidSlug(o.slug); err != nil {
		return fmt.Errorf("--wiki-slug: %w", err)
	}
	date, err := debateDate(o.date, time.Now())
	if err != nil {
		return err
	}
	if _, err := parseNumber(target); err != nil {
		return err
	}
	ro := runOpts{keep: o.keep, plain: true}
	if len(o.actors) > 0 {
		rc, err := openRunContext(g)
		if err != nil {
			return err
		}
		if err := rc.ws.Sync(cmd.Context()); err != nil {
			return err
		}
		ro.synced = true
	}
	out := cmd.OutOrStdout()
	var failed []string
	if len(o.actors) > 0 {
		// The runs start side by side: the server places the first in a
		// new round, or the open one, and the rest join it.
		errs := make([]error, len(o.actors))
		var wg sync.WaitGroup
		for i, a := range o.actors {
			ro := ro
			ro.actor, ro.question = a, o.question
			ro.debate = &server.DebateRequest{Slug: o.slug, Date: date, Raw: o.raw, Question: o.question}
			wg.Go(func() { errs[i] = startRun(cmd, g, policy.Discuss, target, ro) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				failed = append(failed, o.actors[i])
				fmt.Fprintf(cmd.ErrOrStderr(), "debate: %s: %v\n", o.actors[i], err)
			}
		}
		fmt.Fprintf(out, "debate: %d of %d answers in\n", len(o.actors)-len(failed), len(o.actors))
		if len(failed) == len(o.actors) && o.consolidate != "" {
			return errors.New("debate: no run of the round succeeded; not consolidating")
		}
	}
	if o.consolidate != "" {
		ro.actor = o.consolidate
		ro.debate = &server.DebateRequest{Slug: o.slug, Date: date}
		if err := startRun(cmd, g, policy.Consolidate, target, ro); err != nil {
			return fmt.Errorf("debate: consolidate: %w", err)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("debate: the runs of %s did not succeed", strings.Join(failed, ", "))
	}
	return nil
}

// debatePrompt is run's debate round as its prompt shows it, from the
// wiki's files: what the run writes, and what of its debate it may read.
func debatePrompt(run secrets.Run, files []server.WikiFile) *runs.DebatePrompt {
	d := run.Debate
	consolidate := run.Kind == policy.Consolidate
	author := run.Actor
	if consolidate {
		author = wiki.Consolidated
	}
	p := &runs.DebatePrompt{
		Round: d.Round, Question: d.Question,
		File: wiki.Round{Debate: d.Name, N: d.Round, Author: author}.File(),
	}
	if deb, ok := wiki.ParseDebate(d.Name); ok {
		p.Page = wiki.WebPath(deb.Page())
	}
	for _, f := range files {
		r, ok := wiki.ParseRound(f.Path)
		if !ok || r.Debate != d.Name {
			continue
		}
		switch {
		case consolidate && r.N == d.Round && r.Author != wiki.Consolidated:
			p.Answers = append(p.Answers, f.Path)
		case r.N < d.Round && (r.Author == wiki.Consolidated || (d.Raw && !consolidate)):
			p.Earlier = append(p.Earlier, f.Path)
		}
	}
	slices.Sort(p.Answers)
	slices.Sort(p.Earlier)
	return p
}

// consolidatedText is a consolidate run's handover comment: the link to
// the debate's page, which holds the consolidation and every round's
// files.
func consolidatedText(rec runs.Record, run secrets.Run, status string, code int) string {
	var b strings.Builder
	b.WriteString(handoverHeader(rec, status, code))
	if rec.StopRequested {
		b.WriteString("Stopped by the owner.\n\n")
	}
	page := "Review-" + run.Debate.Name
	link := fmt.Sprintf("/%s/%s/wiki/%s", run.Org, run.Repo, page)
	if status == runs.StatusSucceeded {
		fmt.Fprintf(&b, "Round %d of the debate is consolidated: [%s](%s).\n", run.Debate.Round, page, link)
	} else {
		fmt.Fprintf(&b, "Round %d of the debate is not consolidated by this run. The debate so far: [%s](%s).\n", run.Debate.Round, page, link)
	}
	return b.String()
}
