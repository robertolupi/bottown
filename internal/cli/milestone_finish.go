package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/server"
)

func newMilestoneBranch(g *globals) *cobra.Command {
	var repo string
	c := &cobra.Command{
		Use:   "branch <slug> [--repo R]",
		Short: "Create a milestone's integration branch, milestone/<slug>",
		Long: `Create milestone/<slug>, the integration branch the plan's child issues
merge into, from the default branch's head, as the manager account. The
plan must have an Implementation Epic filed by ` + "`bottown milestone apply`" + `.
If the branch exists, branch reports it and succeeds.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, inst, _, name, key, err := ownerRepo(g, repo)
			if err != nil {
				return err
			}
			var out server.MilestoneBranchOut
			api := milestoneClient{c: client.New(inst.ServerURL(), key).WithTimeout(time.Minute)}
			if err := api.call(cmd.Context(), "branch", server.MilestoneIn{Repo: name, Slug: args[0]}, &out); err != nil {
				return err
			}
			verb := "exists"
			if out.Created {
				verb = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s at %s (epic #%d)\n", out.Branch, verb, short(out.SHA), out.Epic)
			return nil
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	return c
}

func newMilestoneFinish(g *globals) *cobra.Command {
	var repo string
	var checkTimeout time.Duration
	c := &cobra.Command{
		Use:   "finish <slug> [--repo R] [--check-timeout D]",
		Short: "Merge the default branch into milestone/<slug>, check it, and open the final PR",
		Long: `Finish a milestone whose active child issues are all closed. The active
children are the issues with the owner's child marker for the plan that
block its Implementation Epic, open or closed; an issue whose key apply
dropped from the plan no longer blocks the epic, and so does not count.

Finish refuses while any active child is open, listing them. Otherwise it
merges the default branch into milestone/<slug> as the manager account (the
only time it is merged in); on a conflict it stops, leaving the branch
unchanged, for the owner to resolve. Then it runs the e2e commands, as
bottown flow does at a PR head: in a clean worktree detached at the
branch's new head, with the same log (.bottown/runs/flow-<epic>-<sha>-<check>.e2e.log)
and time limit. If they pass, it opens the final pull request from
milestone/<slug> into the default branch as the manager, or updates the
description of the one already open: the merged child issues and their
pull requests, and the keyword that closes the epic when the owner merges
it. If they fail, it comments on the epic with the failure and the log's
path, opens no pull request and exits non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := openRunContextRepo(g, repo)
			if err != nil {
				return err
			}
			api := milestoneClient{c: client.New(rc.inst.ServerURL(), rc.key).WithTimeout(5 * time.Minute)}
			return milestoneFinish(cmd.Context(), cmd.OutOrStdout(), api, flowChecker{ws: rc.ws}, checkTimeout, rc.repo, args[0])
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	c.Flags().DurationVar(&checkTimeout, "check-timeout", defaultCheckTimeout, "time limit for the e2e checks; running out is a failure")
	return c
}

// milestoneAPI is the server's side of finish: the owner's milestone
// operations, and a comment on the epic.
type milestoneAPI interface {
	MergeBase(ctx context.Context, in server.MilestoneIn) (server.MilestoneMergeBaseOut, error)
	FinalPR(ctx context.Context, in server.MilestonePRIn) (server.MilestonePROut, error)
	Comment(ctx context.Context, repo string, n int64, body string) error
}

// milestoneFinish is bottown milestone finish: merge the default branch
// into the milestone branch, run the e2e gate at its head, and open or
// update the final PR when it passes, or comment on the epic when not.
func milestoneFinish(ctx context.Context, w io.Writer, api milestoneAPI, checker flow.Checker, timeout time.Duration, repo, slug string) error {
	in := server.MilestoneIn{Repo: repo, Slug: slug}
	m, err := api.MergeBase(ctx, in)
	if err != nil {
		return err
	}
	if m.Merged {
		fmt.Fprintf(w, "merged %s into %s: %s\n", m.Base, m.Branch, short(m.HeadSHA))
	} else {
		fmt.Fprintf(w, "%s already contains %s: %s\n", m.Branch, m.Base, short(m.HeadSHA))
	}
	fmt.Fprintf(w, "e2e checks on %s at %s\n", m.Branch, short(m.HeadSHA))
	o, err := flow.RunCheck(ctx, checker, flow.CheckRequest{Repo: repo, Issue: m.Epic, SHA: m.HeadSHA, Groups: []string{flow.GroupE2E}}, timeout)
	if err != nil {
		return err
	}
	switch {
	case o.Skipped:
		o.Description = "the repository names no e2e commands"
		fmt.Fprintln(w, "no e2e checks: "+o.Description)
	case o.Passed:
		fmt.Fprintf(w, "e2e checks passed: %s; log: %s\n", o.Description, o.Log)
	default:
		summary := strings.TrimSpace(o.Summary)
		if summary == "" {
			summary = o.Description
		}
		body := fmt.Sprintf("**bottown milestone finish:** the e2e checks failed on `%s` at `%s`, so there is no final pull request yet.\n\n```\n%s\n```\n\nLog: `%s`",
			m.Branch, m.HeadSHA, summary, o.Log)
		if err := api.Comment(ctx, repo, m.Epic, body); err != nil {
			return fmt.Errorf("the e2e checks failed at %s (log: %s), and commenting on epic #%d failed: %w", short(m.HeadSHA), o.Log, m.Epic, err)
		}
		return fmt.Errorf("the e2e checks failed on %s at %s: %s\nlog: %s\ncommented on epic #%d; no pull request opened", m.Branch, short(m.HeadSHA), o.Description, o.Log, m.Epic)
	}
	pr, err := api.FinalPR(ctx, server.MilestonePRIn{MilestoneIn: in, SHA: m.HeadSHA, Checks: o.Description})
	if err != nil {
		return err
	}
	verb := "updated"
	if pr.Created {
		verb = "opened"
	}
	fmt.Fprintf(w, "%s !%d from %s into %s: %s\n", verb, pr.Number, m.Branch, m.Base, pr.URL)
	if !pr.Created {
		// A merge since the last finish told the PR its head was not
		// gated (bottown milestone run); say that it is now.
		body := fmt.Sprintf("**bottown milestone finish:** the e2e gate passed at `%s`: the head is gated. The description lists the merged child issues.", m.HeadSHA)
		if err := api.Comment(ctx, repo, pr.Number, body); err != nil {
			return fmt.Errorf("updated !%d at a gated head, but commenting on it failed: %w", pr.Number, err)
		}
	}
	return nil
}

// milestoneClient is milestoneAPI over the server, with the owner's key.
type milestoneClient struct{ c *client.Client }

// call posts in to /v1/forge/milestone/<op>; a refusal is the server's
// message alone.
func (m milestoneClient) call(ctx context.Context, op string, in, out any) error {
	err := m.c.Call(ctx, "POST", "/v1/forge/milestone/"+op, in, out)
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Message != "" {
		return errors.New(ae.Message)
	}
	return err
}

func (m milestoneClient) MergeBase(ctx context.Context, in server.MilestoneIn) (out server.MilestoneMergeBaseOut, err error) {
	return out, m.call(ctx, "merge-base", in, &out)
}

func (m milestoneClient) FinalPR(ctx context.Context, in server.MilestonePRIn) (out server.MilestonePROut, err error) {
	return out, m.call(ctx, "pr", in, &out)
}

func (m milestoneClient) Comment(ctx context.Context, repo string, n int64, body string) error {
	in := server.CommentIn{Target: server.Target{Repo: repo, Number: n}, Body: body}
	return m.c.Call(ctx, "POST", "/v1/forge/comment", in, nil)
}
