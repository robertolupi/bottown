package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/hostctl"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/spf13/cobra"
)

func newRepo(g *globals) *cobra.Command {
	repo := &cobra.Command{Use: "repo", Short: "Manage repositories on an instance"}
	var branch string
	add := &cobra.Command{
		Use:   "add [NAME]",
		Short: "Host the current checkout on the instance under a bottown remote",
		Long: `Create bottown/NAME on the instance (NAME defaults to the repo the instance
remote already points at, else the checkout's directory name, or the fam's name
for a <fam>/main checkout), add a git remote named after the instance ("bottown" for prod,
"bottown-<instance>" otherwise), push the branch, then apply labels and branch
protection. "origin" is never touched: it stays the external upstream.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return repoAdd(cmd.Context(), g, cmd.OutOrStdout(), name, branch)
		},
	}
	add.Flags().StringVar(&branch, "branch", "", "branch to push and protect (default: the current branch)")
	repo.AddCommand(add)
	return repo
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func repoAdd(ctx context.Context, g *globals, out io.Writer, name, branch string) error {
	inst, err := g.loadInstance()
	if err != nil {
		return err
	}
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not inside a git checkout")
	}
	// The repo this checkout already belongs to on the instance, if any. An
	// existing remote is never silently re-pointed at a different repo.
	remote := inst.Remote()
	existing := ""
	if u, err := git(top, "remote", "get-url", remote); err == nil {
		if _, p, ok := strings.Cut(u, "/git/"); ok {
			_, existing, _ = strings.Cut(strings.TrimSuffix(p, ".git"), "/")
		}
	}
	switch {
	case name == "" && existing != "":
		name = existing
	case name == "":
		// <fam>/main is named after the fam, not "main".
		name = strings.ToLower(filepath.Base(top))
		if name == "main" {
			name = strings.ToLower(filepath.Base(filepath.Dir(top)))
		}
	case existing != "" && !strings.EqualFold(name, existing):
		return fmt.Errorf("remote %s already points at bottown repo %q, not %q; change it with `git remote set-url` first if that is intended", remote, existing, name)
	}
	if branch == "" {
		if branch, err = git(top, "symbolic-ref", "--short", "HEAD"); err != nil {
			return errors.New("detached HEAD; pass --branch")
		}
	}
	key, err := ownerKey(g)
	if err != nil {
		return err
	}
	res, err := putRepo(ctx, inst, key, name, branch)
	if err != nil {
		return err
	}
	url := inst.ServerURL() + res.GitPath
	r := lipgloss.NewRenderer(out)
	if have, err := git(top, "remote", "get-url", remote); err != nil {
		if _, err := git(top, "remote", "add", remote, url); err != nil {
			return err
		}
		printRepoRemoteAdded(out, r, remote, url)
	} else if have != url {
		if _, err := git(top, "remote", "set-url", remote, url); err != nil {
			return err
		}
		printRepoRemoteUpdated(out, r, remote, url)
	}
	if err := configureCredentialHelper(top, inst); err != nil {
		return err
	}
	// Associate the checkout with this instance unless it already is with
	// one (a repo can live on several; the first stays the default).
	if _, err := git(top, "config", "--local", "--get", gitInstanceKey); err != nil {
		if _, err := git(top, "config", "--local", gitInstanceKey, inst.Name); err != nil {
			return err
		}
		fmt.Fprintf(out, "set git config %s = %s\n", gitInstanceKey, inst.Name)
	}
	push := exec.CommandContext(ctx, "git", "-C", top, "push", remote, branch)
	push.Stdout, push.Stderr = out, os.Stderr
	if err := push.Run(); err != nil {
		return fmt.Errorf("git push %s %s: %w", remote, branch, err)
	}
	// Now that the branch exists, set it as default and protect it. Forgejo
	// clears the repository's "empty" flag asynchronously after the first
	// push, so wait for that before protection can be applied.
	var res2 repoResult
	for deadline := time.Now().Add(30 * time.Second); ; {
		if res2, err = putRepo(ctx, inst, key, name, branch); err != nil {
			return err
		}
		if !res2.Empty {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still empty on the forge after push; re-run `bottown repo add`", name)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, c := range append(res.Changes, res2.Changes...) {
		fmt.Fprintf(out, "  - %s\n", c)
	}
	printRepoAdded(out, r, name, inst.Name, inst.ForgejoURL()+"/"+res2.FullName)
	return nil
}

func printRepoRemoteAdded(out io.Writer, r *lipgloss.Renderer, remote, url string) {
	st := newStyles(r)
	fmt.Fprintf(out, "added remote %s -> %s\n", st.Bold.Render(remote), st.Info.Render(url))
}

func printRepoRemoteUpdated(out io.Writer, r *lipgloss.Renderer, remote, url string) {
	st := newStyles(r)
	fmt.Fprintf(out, "updated remote %s -> %s\n", st.Bold.Render(remote), st.Info.Render(url))
}

func printRepoAdded(out io.Writer, r *lipgloss.Renderer, name, instName, url string) {
	st := newStyles(r)
	fmt.Fprintf(out, "%s is hosted on bottown %s: %s\n", st.Bold.Render(name), st.Bold.Render(instName), st.Info.Render(url))
}

// configureCredentialHelper makes git ask bottown, and only bottown, for
// credentials to this instance's server URL.
func configureCredentialHelper(top string, inst config.Instance) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	k := "credential." + inst.ServerURL() + ".helper"
	_, _ = git(top, "config", "--local", "--unset-all", k)
	// An empty helper resets helpers inherited from global config (such as
	// osxkeychain), so the key is neither looked up nor stored elsewhere.
	if _, err := git(top, "config", "--local", "--add", k, ""); err != nil {
		return err
	}
	_, err = git(top, "config", "--local", "--add", k, fmt.Sprintf("!%q -i %s credential", self, inst.Name))
	return err
}

type repoResult struct {
	FullName      string   `json:"full_name"`
	Empty         bool     `json:"empty"`
	DefaultBranch string   `json:"default_branch"`
	GitPath       string   `json:"git_path"`
	Changes       []string `json:"changes"`
}

func putRepo(ctx context.Context, inst config.Instance, key, name, branch string) (repoResult, error) {
	var res repoResult
	body, _ := json.Marshal(map[string]string{"default_branch": branch})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, inst.ServerURL()+"/v1/repos/"+name, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return res, fmt.Errorf("create repo %s: %s: %s", name, resp.Status, strings.TrimSpace(string(b)))
	}
	return res, json.NewDecoder(resp.Body).Decode(&res)
}

func ownerKey(g *globals) (string, error) {
	sec, err := g.secrets()
	if err != nil {
		return "", err
	}
	key, ok, err := sec.Get(hostctl.AccountAPIKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no owner API key for instance %s; run `bottown up -i %s`", g.instance, g.instance)
	}
	return key, nil
}

func newCredential(g *globals) *cobra.Command {
	var runFile, keyDir string
	cred := &cobra.Command{
		Use:   "credential (get|store|erase)",
		Short: "git credential helper for the instance's git remote",
		Long: `git credential helper. Without --run-file it returns the owner's key for the
instance's git remote; with --run-file (set up in run worktrees) it returns
that run's key, as the run's actor.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "get" {
				return nil // store/erase: bottown owns the key, nothing to do
			}
			attrs := map[string]string{}
			sc := bufio.NewScanner(cmd.InOrStdin())
			for sc.Scan() {
				if k, v, ok := strings.Cut(sc.Text(), "="); ok {
					attrs[k] = v
				}
			}
			if keyDir != "" {
				// An actor worktree: the active run's key, else the
				// worktree's read-only actor key.
				runFile = filepath.Join(keyDir, "run.json")
				if rec, err := runs.LoadFile(runFile); err != nil || rec.Key == "" {
					runFile = filepath.Join(keyDir, "actor.json")
				}
			}
			if runFile != "" {
				rec, err := runs.LoadFile(runFile)
				if errors.Is(err, os.ErrNotExist) {
					return nil // an actor worktree outside a run has no credentials
				}
				if err != nil {
					return err
				}
				if attrs["protocol"] != "http" || "http://"+attrs["host"] != rec.ServerURL || rec.Key == "" {
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "username=%s\npassword=%s\n", rec.Actor, rec.Key)
				return nil
			}
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			if attrs["protocol"] != "http" || attrs["host"] != fmt.Sprintf("127.0.0.1:%d", inst.ServerPort) {
				return nil
			}
			key, err := ownerKey(g)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "username=%s\npassword=%s\n", inst.OwnerLogin, key)
			return nil
		},
	}
	cred.AddCommand(&cobra.Command{
		Use:   "password",
		Short: "Print the owner's initial Forgejo web password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sec, err := g.secrets()
			if err != nil {
				return err
			}
			pw, ok, err := sec.Get(hostctl.AccountPassword)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("no stored password for this instance")
			}
			fmt.Fprintln(cmd.OutOrStdout(), pw)
			return nil
		},
	})
	var label string
	var full bool
	key := &cobra.Command{
		Use:   "key ACTOR",
		Short: "Mint an actor key: read-only (reads, starting runs) unless --full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			execArgs := []string{"bottown", "admin", "actor-key", args[0], "--label", label}
			if full {
				execArgs = append(execArgs, "--full")
			}
			k, err := g.docker().Exec(cmd.Context(), inst.Container(), "git", execArgs...)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), k)
			return nil
		},
	}
	key.Flags().StringVar(&label, "label", "", "note stored with the key")
	key.Flags().BoolVar(&full, "full", false, "full actor access, unscoped by any run (owner escape hatch)")
	cred.AddCommand(key)
	cred.Flags().StringVar(&runFile, "run-file", "", "answer with the key of the run recorded in this file")
	cred.Flags().StringVar(&keyDir, "key-dir", "", "an actor worktree's key directory: its run key, else its read-only actor key")
	return cred
}
