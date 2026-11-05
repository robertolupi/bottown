package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// blockedPushURL is set as pushurl on every non-bottown remote in actor
// worktrees, so agents cannot push upstream with the owner's credentials.
const blockedPushURL = "bottown-blocked://pushing-from-actor-worktrees-is-disabled"

// Git config keys bottown keeps in an actor worktree's config.worktree.
const (
	gitActorKey = "bottown.actor"
	gitRunKey   = "bottown.run"
)

func newActor(g *globals) *cobra.Command {
	actor := &cobra.Command{
		Use:   "actor",
		Short: "Manage permanent actor worktrees next to the main checkout",
		Long: `Actor worktrees are permanent git worktrees of the main checkout, one per
actor, in the fam layout:

  <fam>/main      the owner's checkout
  <fam>/claude    actor claude
  <fam>/agy       actor agy

Each has the actor's git identity and bottown state in its own worktree config,
credential helpers that never hand out the owner's key, and pushes to every
remote other than bottown blocked.`,
	}
	var path string
	setup := &cobra.Command{
		Use:   "setup ACTOR...",
		Short: "Create or repair actor worktrees (run in the main checkout)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if path != "" && len(args) > 1 {
				return errors.New("--path takes a single actor")
			}
			for _, a := range args {
				if err := actorSetup(cmd, g, a, path); err != nil {
					return fmt.Errorf("%s: %w", a, err)
				}
			}
			return nil
		},
	}
	setup.Flags().StringVar(&path, "path", "", "worktree location (default: next to the main checkout, named after the actor)")
	actor.AddCommand(setup, newActorLs(g))
	return actor
}

// mainCheckout returns the top of the owner's main checkout and refuses
// actor worktrees or run worktrees.
func mainCheckout() (string, error) {
	top, err := runs.Git(".", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("not inside a git checkout")
	}
	if v, _ := runs.Git(top, nil, "config", "--worktree", "--get", gitActorKey); v != "" {
		return "", fmt.Errorf("%s is actor %s's worktree; run this in the main checkout", top, v)
	}
	if strings.Contains(filepath.ToSlash(top), "/.bottown/") {
		return "", errors.New("this is a run worktree; run this in the main checkout")
	}
	return top, nil
}

func actorSetup(cmd *cobra.Command, g *globals, name, path string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	inst, err := g.loadInstance()
	if err != nil {
		return err
	}
	top, err := mainCheckout()
	if err != nil {
		return err
	}
	if _, ok := repoOnInstance(top, inst); !ok {
		return fmt.Errorf("this checkout is not hosted on instance %s; run `bottown -i %s repo add` first", inst.Name, inst.Name)
	}
	key, err := ownerKey(g)
	if err != nil {
		return err
	}
	var actors []server.ActorInfo
	if err := callServer(ctx, inst, key, "GET", "/v1/actors", nil, &actors); err != nil {
		return err
	}
	i := slices.IndexFunc(actors, func(a server.ActorInfo) bool { return a.Name == name })
	if i < 0 {
		names := []string{}
		for _, a := range actors {
			names = append(names, a.Name)
		}
		return fmt.Errorf("not an actor on instance %s (actors: %s)", inst.Name, strings.Join(names, ", "))
	}
	info := actors[i]

	if path == "" {
		if filepath.Base(top) != "main" {
			return fmt.Errorf("the main checkout should be <fam>/main (it is %s); move it, or pass --path", top)
		}
		path = filepath.Join(filepath.Dir(top), name)
	}
	if path, err = filepath.Abs(path); err != nil {
		return err
	}

	// Worktree config must be on before any per-worktree settings exist.
	if _, err := runs.Git(top, nil, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	remote := inst.Remote()
	if _, err := runs.GitCtx(ctx, top, nil, "fetch", "-q", remote); err != nil {
		return err
	}
	base, err := runs.RemoteDefaultBranch(ctx, top, remote)
	if err != nil {
		return err
	}

	switch existing, err := worktreeAt(top, path); {
	case err != nil:
		return err
	case existing:
		fmt.Fprintf(out, "repairing %s's worktree at %s\n", name, path)
	default:
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists and is not a worktree of this checkout", path)
		}
		if _, err := runs.Git(top, nil, "worktree", "add", "-q", "--detach", path, remote+"/"+base); err != nil {
			return err
		}
		fmt.Fprintf(out, "created %s's worktree at %s (detached at %s/%s)\n", name, path, remote, base)
	}

	gitDir, err := runs.Git(path, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	keyDir := filepath.Join(gitDir, "bottown")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	set := func(k, v string) error {
		_, err := runs.Git(path, nil, "config", "--worktree", k, v)
		return err
	}
	for _, kv := range [][2]string{
		{"user.name", name},
		{"user.email", name + "@bottown.local"},
		{gitActorKey, name},
		{"bottown.family", info.Family},
		{"bottown.harness", info.Harness},
		{gitInstanceKey, inst.Name},
		{"push.default", "current"},
	} {
		if err := set(kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := runs.Git(path, nil, "config", "--worktree", "--get", gitRunKey); err != nil {
		if err := set(gitRunKey, ""); err != nil {
			return err
		}
	}

	// Credentials: forget every helper inherited from main and global
	// config (the owner's bottown key, osxkeychain, gh), then answer only
	// with this worktree's run key.
	_, _ = runs.Git(path, nil, "config", "--worktree", "--unset-all", "credential.helper")
	for _, k := range urlScopedHelpers(path) {
		if _, err := runs.Git(path, nil, "config", "--worktree", "--replace-all", k, ""); err != nil {
			return err
		}
	}
	if err := set("credential.helper", ""); err != nil {
		return err
	}
	if err := ensureActorKey(cmd, g, inst, name, path, keyDir); err != nil {
		return err
	}
	helper := fmt.Sprintf("!%q credential --key-dir %q", self, keyDir)
	if _, err := runs.Git(path, nil, "config", "--worktree", "--add", "credential.helper", helper); err != nil {
		return err
	}

	// Pushes: only to the bottown remote, and only with a run key.
	remotes, err := runs.Git(top, nil, "remote")
	if err != nil {
		return err
	}
	var blocked []string
	for _, r := range strings.Fields(remotes) {
		if r == remote {
			continue
		}
		if err := set("remote."+r+".pushurl", blockedPushURL); err != nil {
			return err
		}
		blocked = append(blocked, r)
	}
	printActorSetup(out, lipgloss.NewRenderer(out), name, info.Family, info.Harness, inst.Name, blocked)
	return nil
}

// ensureActorKey keeps a read-only actor key in the worktree's git dir
// (<gitdir>/bottown/actor.json, 0600), so the worktree can read and start
// runs outside a run. A key that no longer works is replaced.
func ensureActorKey(cmd *cobra.Command, g *globals, inst config.Instance, actor, path, keyDir string) error {
	file := filepath.Join(keyDir, "actor.json")
	if rec, err := runs.LoadFile(file); err == nil && rec.Key != "" {
		if who, err := whoami(cmd.Context(), inst, rec.Key); err == nil && who == actor {
			return nil
		}
	}
	key, err := g.docker().Exec(cmd.Context(), inst.Container(), "git", "bottown", "admin", "actor-key", actor, "--label", "worktree "+path)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(runs.Record{Actor: actor, Instance: inst.Name, Key: strings.TrimSpace(key), ServerURL: inst.ServerURL()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, b, 0o600)
}

// urlScopedHelpers lists credential.<url>.helper keys visible in dir (from
// main's or global config), so the worktree can reset each of them.
func urlScopedHelpers(dir string) []string {
	out, err := runs.Git(dir, nil, "config", "--name-only", "--get-regexp", `^credential\..+\.helper$`)
	if err != nil {
		return nil
	}
	var keys []string
	for _, k := range strings.Fields(out) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// worktreeAt reports whether path is a worktree of the checkout at top.
func worktreeAt(top, path string) (bool, error) {
	out, err := runs.Git(top, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want, _ := filepath.EvalSymlinks(path)
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			if p == path {
				return true, nil
			}
			if real, err := filepath.EvalSymlinks(p); err == nil && real == want {
				return true, nil
			}
		}
	}
	return false, nil
}

// actorWorktree is one actor worktree of the main checkout.
type actorWorktree struct {
	Actor, Path, Head, Run string
}

func listActorWorktrees(top string) ([]actorWorktree, error) {
	out, err := runs.Git(top, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []actorWorktree
	for _, block := range strings.Split(out, "\n\n") {
		var wt actorWorktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch "):
				wt.Head = strings.TrimPrefix(line, "branch refs/heads/")
			case line == "detached":
				wt.Head = "(detached)"
			}
		}
		if wt.Path == "" {
			continue
		}
		wt.Actor, _ = runs.Git(wt.Path, nil, "config", "--worktree", "--get", gitActorKey)
		if wt.Actor == "" {
			continue
		}
		wt.Run, _ = runs.Git(wt.Path, nil, "config", "--worktree", "--get", gitRunKey)
		if wt.Head == "(detached)" {
			if sha, err := runs.Git(wt.Path, nil, "rev-parse", "--short", "HEAD"); err == nil {
				wt.Head = sha + " (detached)"
			}
		}
		wts = append(wts, wt)
	}
	return wts, nil
}

func newActorLs(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the main checkout's actor worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			top, err := mainCheckout()
			if err != nil {
				return err
			}
			wts, err := listActorWorktrees(top)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			return printActorList(out, lipgloss.NewRenderer(out), wts)
		},
	}
}

func printActorList(out io.Writer, r *lipgloss.Renderer, wts []actorWorktree) error {
	st := newStyles(r)
	tw := newTableWriter(out)
	tw.Row(
		st.Header.Render("ACTOR"),
		st.Header.Render("RUN"),
		st.Header.Render("HEAD"),
		st.Header.Render("PATH"),
	)
	for _, wt := range wts {
		run := wt.Run
		styledRun := run
		if run == "" {
			run = "-"
			styledRun = st.Dim.Render("-")
		} else {
			styledRun = st.Info.Bold(true).Render(run)
		}
		tw.Row(
			st.Info.Bold(true).Render(wt.Actor),
			styledRun,
			st.Bold.Render(wt.Head),
			st.Dim.Render(wt.Path),
		)
	}
	return tw.Flush()
}

func printActorSetup(out io.Writer, r *lipgloss.Renderer, name, family, harness, instName string, blocked []string) {
	st := newStyles(r)
	fmt.Fprintf(out, "%s (%s, %s): identity %s <%s@bottown.local>, instance %s",
		st.Info.Bold(true).Render(name),
		st.Dim.Render(family),
		st.Dim.Render(harness),
		st.Bold.Render(name),
		name,
		st.Bold.Render(instName),
	)
	if len(blocked) > 0 {
		fmt.Fprintf(out, ", pushes blocked to %s", st.Warning.Render(strings.Join(blocked, ", ")))
	}
	fmt.Fprintln(out)
}
