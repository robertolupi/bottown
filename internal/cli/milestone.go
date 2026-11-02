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
	"github.com/spf13/cobra"
)

func newMilestone() *cobra.Command {
	m := &cobra.Command{
		Use:   "milestone",
		Short: "Milestone plans: the files that describe a milestone's issues",
	}
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
	return m
}

// milestoneCheck checks the plan file arg names, from the directory dir. It
// prints the diagnostics to errw and fails if there are any.
func milestoneCheck(out, errw io.Writer, dir, arg string) error {
	top, topErr := git(dir, "rev-parse", "--show-toplevel")

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
		if _, err := os.Stat(filepath.Join(top, filepath.FromSlash(plan.Design))); err != nil {
			diags = append(diags, milestone.Diagnostic{File: name, Line: plan.DesignLine,
				Msg: fmt.Sprintf("front matter: design %q is not in the checkout", plan.Design)})
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
