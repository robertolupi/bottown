package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/runs"
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
	return m
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
