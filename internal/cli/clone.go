package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

func newClone(g *globals) *cobra.Command {
	var name, origin string
	var actors []string
	cmd := &cobra.Command{
		Use:   "clone SRC FAMDIR",
		Short: "Bootstrap a fam: FAMDIR/main from SRC, hosted on the instance, plus actor worktrees",
		Long: `Bootstrap the fam layout for a repository:

  FAMDIR/main      a clone of SRC (a local checkout or a URL), hosted on the
                   instance as bottown/NAME (NAME defaults to FAMDIR's name)
  FAMDIR/<actor>   a permanent worktree per actor

main's origin is the true upstream: SRC's own origin when SRC is a local
checkout, --origin when given, or SRC itself when it is a URL. A local path is
never used as origin. The branch checked out in SRC (with any unpushed commits)
becomes the default branch on the instance.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return clone(cmd, g, args[0], args[1], name, origin, actors)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "repository name on the instance (default: FAMDIR's base name)")
	cmd.Flags().StringVar(&origin, "origin", "", "upstream URL for main's origin (default: SRC's origin, or SRC if it is a URL)")
	cmd.Flags().StringSliceVar(&actors, "actors", nil, "actors to create worktrees for (default: all of the instance's actors)")
	return cmd
}

func isURL(s string) bool {
	return strings.Contains(s, "://") || (strings.Contains(s, "@") && strings.Contains(s, ":") && !strings.HasPrefix(s, "/"))
}

func clone(cmd *cobra.Command, g *globals, src, famDir, name, origin string, actors []string) error {
	out := cmd.OutOrStdout()
	inst, err := g.loadInstance()
	if err != nil {
		return err
	}
	if famDir, err = filepath.Abs(famDir); err != nil {
		return err
	}
	if ents, err := os.ReadDir(famDir); err == nil && len(ents) > 0 {
		return fmt.Errorf("%s exists and is not empty", famDir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if name == "" {
		name = strings.ToLower(filepath.Base(famDir))
	}

	// The true upstream for main's origin.
	if origin == "" {
		if isURL(src) {
			origin = src
		} else if u, err := runs.Git(src, nil, "remote", "get-url", "origin"); err == nil {
			origin = u
		}
	}
	if !isURL(src) {
		if src, err = filepath.Abs(src); err != nil {
			return err
		}
		if _, err := runs.Git(src, nil, "rev-parse", "--show-toplevel"); err != nil {
			return fmt.Errorf("%s is not a git checkout", src)
		}
		if st, err := runs.Git(src, nil, "status", "--porcelain"); err == nil && st != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s has uncommitted changes; only committed history is cloned\n", src)
		}
	}

	key, err := ownerKey(g)
	if err != nil {
		return err
	}
	var known []server.ActorInfo
	if err := callServer(cmd.Context(), inst, key, "GET", "/v1/actors", nil, &known); err != nil {
		return err
	}
	if len(actors) == 0 {
		for _, a := range known {
			actors = append(actors, a.Name)
		}
	}

	main := filepath.Join(famDir, "main")
	if err := os.MkdirAll(famDir, 0o755); err != nil {
		return err
	}
	if _, err := runs.Git(famDir, nil, "clone", "-q", src, main); err != nil {
		return err
	}
	switch {
	case origin == "":
		if _, err := runs.Git(main, nil, "remote", "remove", "origin"); err != nil {
			return err
		}
		fmt.Fprintf(out, "cloned %s into %s (no upstream origin; add one with `git remote add origin URL`)\n", src, main)
	case origin != src:
		if _, err := runs.Git(main, nil, "remote", "set-url", "origin", origin); err != nil {
			return err
		}
		// Refresh origin's refs from the real upstream; ignore failures
		// (offline, or a private upstream), the clone itself is complete.
		if _, err := runs.Git(main, nil, "fetch", "-q", "--prune", "origin"); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: fetch from origin %s: %v\n", origin, err)
		}
		fmt.Fprintf(out, "cloned %s into %s (origin %s)\n", src, main, origin)
	default:
		fmt.Fprintf(out, "cloned %s into %s\n", src, main)
	}

	// The rest works on the new main checkout.
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(main); err != nil {
		return err
	}
	defer os.Chdir(prev)
	if err := repoAdd(cmd.Context(), g, out, name, ""); err != nil {
		return err
	}
	for _, a := range actors {
		if err := actorSetup(cmd, g, a, ""); err != nil {
			return fmt.Errorf("actor %s: %w", a, err)
		}
	}
	fmt.Fprintf(out, "\n%s is ready: cd %s/<actor> and start a harness there, e.g. /bottown-implement 12\n", famDir, famDir)
	return nil
}
