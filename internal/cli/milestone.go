package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/land"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/spf13/cobra"
)

func newMilestone(g *globals) *cobra.Command {
	m := &cobra.Command{
		Use:   "milestone",
		Short: "Milestone plans: the files that describe a milestone's issues",
	}
	// The same command as `run plan`, where milestones start.
	m.AddCommand(newRunPlan(g))
	m.AddCommand(&cobra.Command{
		Use:   "check <slug|path>",
		Short: "Check a milestone plan file, offline",
		Long: `Check a milestone plan file against the plan format, offline: it never
contacts the server or Forgejo.

An argument containing / or ending in .md is the file's path. Otherwise it is
the plan's slug, and the file is <milestones_dir>/<slug>.md in the checkout,
where milestones_dir comes from the checkout's .bottown.toml (default
docs/milestones). Inside a checkout, check also verifies that the plan's
design document exists.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return milestoneCheck(cmd.OutOrStdout(), cmd.ErrOrStderr(), ".", args[0])
		},
	})
	m.AddCommand(newMilestoneApply(g))
	m.AddCommand(newMilestoneBranch(g))
	m.AddCommand(newMilestoneMerge(g))
	m.AddCommand(newMilestoneFinish(g))
	m.AddCommand(newMilestoneRun(g))
	return m
}

func newMilestoneMerge(g *globals) *cobra.Command {
	var sha, repo string
	var checkTimeout time.Duration
	c := &cobra.Command{
		Use:   "merge <issue> [--sha SHA] [--check-timeout D] [--repo R]",
		Short: "Land a milestone child's approved PR on its milestone branch, as the manager",
		Long: `Land the open pull request from issue/<issue> on its milestone/* base, as
milestone run does (internal/land, the milestone policy): build the
landing commit, the base's tip merged with the PR's head; run the check
group at it, recording bottown/check there; and merge it fast-forward-only
as the manager account, which closes the issue and comments on it with
what was merged. So a child merged by hand is checked at the combined
head too. Land refuses unless the review assessment reports the PR
approved (a verifier of another family than the author's, or the owner),
and stops, merging nothing, when the base conflicts with the head, the
approval does not carry to the landing commit, or the checks fail there.
When the base moves during the checks, it builds and checks the landing
again. --sha, if given, is the head the PR must be at.

Landing again reports the PR as already merged. Owner only.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issue, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
			if err != nil || issue <= 0 {
				return fmt.Errorf("invalid issue number %q", args[0])
			}
			rc, err := openRunContextRepo(g, repo)
			if err != nil {
				return err
			}
			c := client.New(rc.inst.ServerURL(), rc.key).WithTimeout(5 * time.Minute)
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "landing #%d's PR: checking the combined head before the merge\n", issue)
			l, err := milestoneLand(cmd.Context(), c, flowChecker{ws: rc.ws}, rc.ws.RunsDir, checkTimeout, rc.repo, issue, sha)
			if err != nil {
				return err
			}
			fmt.Fprintln(w, l.Reason)
			if l.Kind != milestone.Landed {
				return fmt.Errorf("landing #%d stopped: %s", issue, l.Reason)
			}
			fmt.Fprintf(w, "merged !%d into %s at %s as the manager\n", l.Merge.PR, l.Merge.Base, shortSHA(l.Merge.HeadSHA))
			return nil
		},
	}
	c.Flags().StringVar(&sha, "sha", "", "the head the PR must be at (default: its current head)")
	c.Flags().DurationVar(&checkTimeout, "check-timeout", defaultCheckTimeout, "the time limit for the checks at the landing commit")
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	return c
}

// milestoneLand lands child issue's PR on its milestone branch through
// land.Land with the milestone policy: the check group at the landing
// commit, then the fast-forward-only merge as the manager, which closes
// the child. The PR's head must be sha, unless sha is empty. A PR merged
// already is reported as landed, and not checked again.
func milestoneLand(ctx context.Context, c *client.Client, checker flow.Checker, runsDir string, checkTimeout time.Duration, repo string, issue int64, sha string) (milestone.Landing, error) {
	pr, err := childPR(ctx, c, repo, issue)
	if err != nil {
		return milestone.Landing{}, err
	}
	merge := milestone.Merge{PR: pr.Number, HeadSHA: pr.HeadSHA, Base: pr.BaseBranch}
	if pr.Merged {
		return milestone.Landing{Kind: milestone.Landed, Merge: merge, Reason: fmt.Sprintf("!%d was already merged", pr.Number)}, nil
	}
	if sha != "" && sha != pr.HeadSHA {
		return milestone.Landing{}, fmt.Errorf("!%d's head is %s, not %s", pr.Number, shortSHA(pr.HeadSHA), shortSHA(sha))
	}
	oc := land.Land(ctx, c, checker, land.Policy{Repo: repo, RunsDir: runsDir, Kind: land.Milestone, CheckTimeout: checkTimeout},
		server.PROut{Number: pr.Number, HeadBranch: pr.HeadBranch, HeadSHA: pr.HeadSHA, BaseBranch: pr.BaseBranch})
	l := milestone.Landing{Kind: milestone.LandFailed, Merge: merge, Reason: oc.String()}
	switch o := oc.(type) {
	case land.Landed:
		l.Kind, l.Merge.HeadSHA = milestone.Landed, o.Head
	case land.Conflict, land.NeedsVerify:
		l.Kind = milestone.LandRepair
	case land.ChecksFailed:
		l.Kind = milestone.LandChecksFailed
	}
	return l, nil
}

// childPR is issue's open PR from issue/<issue>, or, with none open, its
// merged one.
func childPR(ctx context.Context, c *client.Client, repo string, issue int64) (server.PRItem, error) {
	q := url.Values{"repo": {repo}, "state": {"all"}}
	var prs []server.PRItem
	if err := c.Call(ctx, "GET", "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
		return server.PRItem{}, err
	}
	branch := fmt.Sprintf("issue/%d", issue)
	var pr *server.PRItem
	for i := range prs {
		p := &prs[i]
		if p.HeadBranch != branch || (p.State != "open" && !p.Merged) {
			continue
		}
		// An open PR wins over a merged one: merging reports the latter
		// as merged.
		if pr == nil || (p.State == "open" && pr.State != "open") {
			pr = p
		} else if p.State == "open" && pr.State == "open" {
			return server.PRItem{}, fmt.Errorf("more than one open PR from %s", branch)
		}
	}
	if pr == nil {
		return server.PRItem{}, fmt.Errorf("no open pull request from %s", branch)
	}
	return *pr, nil
}

func newMilestoneApply(g *globals) *cobra.Command {
	var o milestone.ApplyOptions
	var repo string
	c := &cobra.Command{
		Use:   "apply <slug> [--dry-run] [--force] [--repo R]",
		Short: "File a merged milestone plan's issues: the epic, its child issues and their dependencies",
		Long: `Apply the milestone plan <milestones_dir>/<slug>.md, as merged on the
repository's default branch on the forge (never the working tree), as the
owner. It files each section as a child issue and an Implementation Epic
listing them, all in the kickoff issue's open milestone, and makes Forgejo
dependencies match the plan: each child is blocked by its depends, and the
epic by every child.

Apply is idempotent: issues carry hidden markers mapping them to the plan,
so re-applying an unchanged plan writes nothing, and re-applying after a
failure resumes it. Every check runs before the first write. A changed
section updates its issue unless the issue is closed, or was edited in
Forgejo since the last apply (--force overwrites it). Sections removed from
the plan keep their issues, which apply flags for the owner.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, inst, org, name, key, err := ownerRepo(g, repo)
			if err != nil {
				return err
			}
			f := newProxyForge(inst.ServerURL(), key, org, name)
			if o.Owner, err = f.ownerLogin(cmd.Context()); err != nil {
				return fmt.Errorf("finding the owner's login: %w", err)
			}
			o.Org, o.Repo, o.Slug, o.Out = org, name, args[0], cmd.OutOrStdout()
			_, err = milestone.Apply(cmd.Context(), f, o)
			return err
		},
	}
	c.Flags().BoolVar(&o.DryRun, "dry-run", false, "check the plan and print the changes, writing nothing")
	c.Flags().BoolVar(&o.Force, "force", false, "overwrite open issues edited in Forgejo since the last apply")
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	return c
}

// milestoneCheck checks the plan file arg names, from the directory dir. It
// prints the diagnostics to errw and fails if there are any.
func milestoneCheck(out, errw io.Writer, dir, arg string) error {
	top, topErr := runs.Git(dir, nil, "rev-parse", "--show-toplevel")

	var file, name string // the file to read, and how diagnostics name it
	if strings.Contains(arg, "/") || strings.HasSuffix(arg, ".md") {
		file, name = arg, arg
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
	} else {
		if !milestone.ValidSlug(arg) {
			return fmt.Errorf("plan slug %q must be lowercase words joined by - (or a path to the file)", arg)
		}
		if topErr != nil {
			return fmt.Errorf("finding the checkout for plan %q: %w", arg, topErr)
		}
		settings := config.DefaultRepoSettings()
		switch b, err := os.ReadFile(filepath.Join(top, config.RepoSettingsFile)); {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		default:
			if settings, err = config.ParseRepoSettings(b); err != nil {
				return err
			}
		}
		name = settings.MilestonesDir + "/" + arg + ".md"
		file = filepath.Join(top, filepath.FromSlash(name))
	}

	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	plan, err := milestone.Check(name, src)
	var diags milestone.Diagnostics
	if err != nil && !errors.As(err, &diags) {
		return err
	}
	if topErr == nil && plan.Design != "" && config.CheckRepoPath(plan.Design) == nil {
		switch fi, err := os.Stat(filepath.Join(top, filepath.FromSlash(plan.Design))); {
		case err != nil:
			diags = append(diags, milestone.Diagnostic{File: name, Line: plan.DesignLine,
				Msg: fmt.Sprintf("front matter: design %q is not in the checkout", plan.Design)})
		case !fi.Mode().IsRegular():
			diags = append(diags, milestone.Diagnostic{File: name, Line: plan.DesignLine,
				Msg: fmt.Sprintf("front matter: design %q is not a file", plan.Design)})
		}
	}
	if len(diags) > 0 {
		for _, d := range diags {
			fmt.Fprintln(errw, d)
		}
		return fmt.Errorf("%s: %d problem(s)", name, len(diags))
	}
	fmt.Fprintf(out, "%s: ok, %d issue(s)\n", name, len(plan.Issues))
	return nil
}
