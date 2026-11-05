package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

func newGuide() *cobra.Command {
	return &cobra.Command{
		Use:   "guide",
		Short: "Print the fam guide: how agents work in this fam, for any project",
		Long: `Print the fam guide for the checkout in the working directory: the fam
layout with its real paths, the bottown remote, how runs start, and what is
allowed outside one. It covers what every project in a fam shares; the
project's own AGENTS.md covers the project.

bottown writes the guide into each actor worktree, untracked, as the file its
harness loads by itself next to AGENTS.md (claude: CLAUDE.local.md, crush:
CRUSH.local.md, agy: .agents/rules/bottown.md, kimi: .kimi-code/AGENTS.md),
from ` + "`actor setup`" + ` and on every ` + "`run start`" + `. Harnesses without such a slot
(codex) find it through the bottown skill that ` + "`harness setup`" + ` installs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			top, err := runs.Git(".", nil, "rev-parse", "--show-toplevel")
			if err != nil {
				return errors.New("not inside a git checkout")
			}
			d, _, err := famGuide(top)
			if err != nil {
				return err
			}
			s, err := runs.Guide(d)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), s)
			return err
		},
	}
}

// famGuide gathers the guide's data for the checkout at top (the owner's
// checkout, an actor worktree or a run worktree), and the harness of its
// actor, if it is an actor worktree.
func famGuide(top string) (runs.GuideData, string, error) {
	d := runs.GuideData{Bottown: selfPath()}
	main, err := famMain(top)
	if err != nil {
		return d, "", err
	}
	d.Main = main
	wts, err := listActorWorktrees(main)
	if err != nil {
		return d, "", err
	}
	for _, wt := range wts {
		d.Actors = append(d.Actors, runs.GuideActor{Name: wt.Actor, Path: wt.Path})
	}
	inst, _ := runs.Git(top, nil, "config", "--get", gitInstanceKey)
	if inst == "" {
		inst = config.DefaultInstance
	}
	d.Remote = config.Instance{Name: inst}.Remote()
	if u, err := runs.Git(top, nil, "remote", "get-url", d.Remote); err == nil {
		if _, p, ok := strings.Cut(u, "/git/"); ok {
			d.Repo = strings.TrimSuffix(p, ".git")
		}
	}
	d.Actor, _ = runs.Git(top, nil, "config", "--worktree", "--get", gitActorKey)
	harness := ""
	if d.Actor != "" {
		harness, _ = runs.Git(top, nil, "config", "--worktree", "--get", "bottown.harness")
		if harness == "" {
			harness = d.Actor
		}
	}
	return d, harness, nil
}

// famMain is the owner's checkout of the fam top belongs to: the main
// worktree of its repository, or, for a run worktree of the runs mirror
// (<main>/.bottown/...), the checkout the mirror is in.
func famMain(top string) (string, error) {
	out, err := runs.Git(top, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	main, ok := strings.CutPrefix(first, "worktree ")
	if !ok {
		return "", fmt.Errorf("cannot find the main worktree of %s", top)
	}
	if before, _, ok := strings.Cut(filepath.ToSlash(main), "/.bottown/"); ok {
		main = filepath.FromSlash(before)
	}
	return main, nil
}

// writeGuide writes (or refreshes) the fam guide in the actor worktree dir,
// in its harness's slot, and says what it did on out. Failing to is not
// fatal to the caller: warn reports it.
func writeGuide(out, warn io.Writer, dir string) {
	d, harness, err := famGuide(dir)
	if err == nil && d.Actor == "" {
		err = fmt.Errorf("%s is not an actor worktree", dir)
	}
	var guide, path, skipped string
	if err == nil {
		guide, err = runs.Guide(d)
	}
	if err == nil {
		path, skipped, err = runs.WriteGuide(dir, harness, guide)
	}
	switch {
	case err != nil:
		fmt.Fprintf(warn, "warning: fam guide: %v\n", err)
	case skipped != "":
		fmt.Fprintf(warn, "fam guide not written: %s\n", skipped)
	case out != nil:
		fmt.Fprintf(out, "fam guide in %s\n", filepath.Join(dir, path))
	}
}

// refreshGuides writes the fam guide into every actor worktree of the main
// checkout top: each one lists the others, so a new actor changes them all.
func refreshGuides(out, warn io.Writer, top string) {
	wts, err := listActorWorktrees(top)
	if err != nil {
		fmt.Fprintf(warn, "warning: fam guide: %v\n", err)
		return
	}
	for _, wt := range wts {
		writeGuide(out, warn, wt.Path)
	}
}

// guideStatus is the state of the fam guide in the actor worktree dir, for
// the web UI's health check (runs.GuideStatus), or an error's text.
func guideStatus(dir string) string {
	d, harness, err := famGuide(dir)
	if err != nil {
		return err.Error()
	}
	if d.Actor == "" {
		return ""
	}
	guide, err := runs.Guide(d)
	if err != nil {
		return err.Error()
	}
	return runs.GuideStatus(dir, harness, guide)
}
