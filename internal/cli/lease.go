package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// Runs leased by a permanent actor worktree: `bottown run start` inside the
// worktree (e.g. from a /bottown-implement command) starts a run for the
// worktree's actor, prepares the checkout and prints the goal prompt;
// `bottown run end` hands over and revokes the key.

func newRunStart() *cobra.Command {
	return &cobra.Command{
		Use:   "start implement|verify|discuss [issue|pr] N",
		Short: "Start a run for this actor worktree and print its goal prompt (agent CLI)",
		Long: `Start a run for this actor worktree and print its goal prompt. The number may
be written 12, #12 or !12, optionally after "issue", "pr" or "pull request", so
slash commands can pass what the user typed: /bottown-implement issue 12.
Each argument is split on spaces, so it may also arrive as one quoted word.

A discuss run reviews and discusses an issue (or pull request) without
implementing it: the worktree is detached at the default branch, and the
run may read and comment, but not push, open pull requests or review.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
			if kind != policy.Implement && kind != policy.Verify && kind != policy.Discuss {
				return fmt.Errorf("kind must be %s, %s or %s", policy.Implement, policy.Verify, policy.Discuss)
			}
			n, err := runTarget(kind, args[1:])
			if err != nil {
				return err
			}
			return runStart(cmd, kind, n)
		},
	}
}

// runTarget parses run start's number from what the user typed: 12, #12 or
// !12, optionally after "issue", "pr" or "pull request". Slash commands pass
// it as one quoted word ("issue #12"), so each argument is split too.
func runTarget(kind string, args []string) (int64, error) {
	var rest []string
	for _, a := range args {
		for _, w := range strings.Fields(a) {
			switch strings.ToLower(w) {
			case "issue", "pr", "pull", "request":
			default:
				rest = append(rest, w)
			}
		}
	}
	if len(rest) != 1 {
		return 0, fmt.Errorf("give one issue or pull request number, e.g. `run start %s 12`", kind)
	}
	return parseNumber(rest[0])
}

// worktreeRepo returns the bottown repo name the actor worktree's remote
// points at (".../git/<org>/<name>.git").
func worktreeRepo(a *agentCtx) (string, error) {
	u, err := git(a.Worktree, "remote", "get-url", a.Remote)
	if err != nil {
		return "", fmt.Errorf("no bottown remote %q in this worktree", a.Remote)
	}
	_, path, ok := strings.Cut(u, "/git/")
	if !ok {
		return "", fmt.Errorf("remote %s (%s) is not a bottown git URL", a.Remote, u)
	}
	_, name, _ := strings.Cut(strings.TrimSuffix(path, ".git"), "/")
	return name, nil
}

func runStart(cmd *cobra.Command, kind string, n int64) (err error) {
	a, err := resolveAgent()
	if err != nil {
		return err
	}
	switch {
	case a.Worktree == "":
		return errors.New("`bottown run start` works in an actor worktree; this is a launched run")
	case a.RunID != "":
		return fmt.Errorf("this worktree is already in run %s; finish it with `bottown run end`", a.RunID)
	}
	if st, err := git(a.Worktree, "status", "--porcelain"); err != nil {
		return err
	} else if st != "" {
		return fmt.Errorf("worktree %s has uncommitted changes; commit, stash or clean them first", a.Worktree)
	}
	repo, err := worktreeRepo(a)
	if err != nil {
		return err
	}

	req := server.RunRequest{Kind: kind, Repo: repo, Interactive: true}
	if kind == policy.Verify {
		req.PR = n
	} else {
		req.Issue = n
	}
	var created server.RunResponse
	if err := a.call("POST", "/v1/runs", req, &created); err != nil {
		return err
	}
	run := created.Run
	// If preparing the worktree fails, end the run rather than leave a live key.
	defer func() {
		if err != nil {
			rk := *a
			rk.Key = created.Key
			_ = rk.call("DELETE", "/v1/runs/"+run.ID, map[string]string{"status": "revoked"}, nil)
		}
	}()

	if _, err := runs.GitCtx(cmd.Context(), a.Worktree, nil, "fetch", "-q", "--prune", a.Remote); err != nil {
		return err
	}
	if kind == policy.Implement {
		start := a.Remote + "/" + run.Branch
		if _, err := git(a.Worktree, "rev-parse", "--verify", "-q", "refs/remotes/"+start); err != nil {
			top, _ := git(a.Worktree, "rev-parse", "--show-toplevel")
			base, err := remoteDefaultBranch(top, a.Remote)
			if err != nil {
				return err
			}
			start = a.Remote + "/" + base
		}
		if _, err := git(a.Worktree, "checkout", "-q", "-B", run.Branch, start); err != nil {
			return err
		}
		for _, kv := range [][2]string{
			{"branch." + run.Branch + ".remote", a.Remote},
			{"branch." + run.Branch + ".merge", "refs/heads/" + run.Branch},
		} {
			if _, err := git(a.Worktree, "config", kv[0], kv[1]); err != nil {
				return err
			}
		}
	} else {
		if _, err := git(a.Worktree, "checkout", "-q", "--detach", run.SHA); err != nil {
			return err
		}
	}

	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: true, Harness: run.Harness, Status: runs.StatusRunning, Started: time.Now().UTC(),
		Worktree: a.Worktree, Key: created.Key, ServerURL: a.ServerURL,
	}
	if inst, _ := git(a.Worktree, "config", "--get", gitInstanceKey); inst != "" {
		rec.Instance = inst
	}
	if err := saveRecord(filepath.Join(a.KeyDir, "run.json"), rec); err != nil {
		return err
	}
	if _, err := git(a.Worktree, "config", "--worktree", gitRunKey, run.ID); err != nil {
		return err
	}

	// The goal prompt, from the issue or PR text.
	rk := *a
	rk.Key = created.Key
	var item server.IssueOut
	if err := rk.call("GET", fmt.Sprintf("/v1/forge/issue?number=%d", n), nil, &item); err != nil {
		return err
	}
	top, _ := git(a.Worktree, "rev-parse", "--show-toplevel")
	base, _ := remoteDefaultBranch(top, a.Remote)
	self, _ := os.Executable()
	prompt, err := runs.Prompt(run.Kind, runs.PromptData{
		RunID: run.ID, Actor: run.Actor, Family: run.Family, Repo: run.Org + "/" + run.Repo,
		Issue: run.Issue, PR: run.PR, Title: item.Title, Body: item.Body,
		Branch: run.Branch, Base: base, SHA: run.SHA, Author: run.Author, AllowSameFamily: run.AllowSameFamily,
		Bottown: self, Remote: a.Remote, SelfEnd: true,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(a.KeyDir, "prompt.md"), []byte(prompt), 0o600); err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	return nil
}

func saveRecord(path string, rec runs.Record) error {
	ws := &runs.Workspace{RunsDir: filepath.Dir(path)}
	tmp := rec
	if err := ws.Save(tmp); err != nil {
		return err
	}
	// Workspace.Save names the file after the run; actor worktrees keep a
	// fixed name.
	return os.Rename(ws.Path(rec.ID, ".json"), path)
}

func newRunEnd() *cobra.Command {
	var failed bool
	var summary, summaryFile string
	cmd := &cobra.Command{
		Use:   "end [--failed] [--summary S | --summary-file F|-]",
		Short: "End this worktree's run: post the handover and revoke the run key (agent CLI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			if a.Worktree == "" || a.RunID == "" {
				return errors.New("no run to end in this worktree")
			}
			file := filepath.Join(a.KeyDir, "run.json")
			rec, err := runs.LoadFile(file)
			if err != nil {
				return err
			}
			text, err := readBody(cmd, summary, summaryFile)
			if err != nil {
				return err
			}
			status := runs.StatusSucceeded
			if failed {
				status = runs.StatusFailed
			}
			if err := a.call("POST", "/v1/forge/comment", server.CommentIn{Body: handoverText(rec, status, 0, text)}, nil); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: handover comment failed: %v\n", err)
			}
			if err := a.call("DELETE", "/v1/runs/"+rec.ID, map[string]string{"status": status}, nil); err != nil {
				return err
			}
			now := time.Now().UTC()
			rec.Status, rec.Ended, rec.Key = status, &now, ""
			if err := saveRecord(file, rec); err != nil {
				return err
			}
			if _, err := git(a.Worktree, "config", "--worktree", gitRunKey, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %s %s; key revoked\n", rec.ID, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failed, "failed", false, "mark the run failed (e.g. blocked)")
	cmd.Flags().StringVar(&summary, "summary", "", "handover summary")
	cmd.Flags().StringVar(&summaryFile, "summary-file", "", "read the handover summary from a file, or - for stdin")
	return cmd
}
