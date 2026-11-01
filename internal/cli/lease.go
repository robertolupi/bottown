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
	"github.com/robertolupi/bottown/internal/secrets"
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
	if a.Worktree != "" {
		if _, err := recoverLease(a); err != nil {
			return err
		}
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
	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: true, Harness: run.Harness, Status: runs.StatusPreparing, Started: time.Now().UTC(),
		Worktree: a.Worktree, Key: created.Key, ServerURL: a.ServerURL,
	}
	if inst, _ := git(a.Worktree, "config", "--get", gitInstanceKey); inst != "" {
		rec.Instance = inst
	}
	return prepareLease(a, rec, func() (err error) {
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
		rec.Status = runs.StatusRunning
		if err := saveRecord(filepath.Join(a.KeyDir, "run.json"), rec); err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), prompt)
		return err
	})
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
			if a.Worktree != "" {
				recovered, err := recoverLease(a)
				if err != nil {
					return err
				}
				if recovered {
					fmt.Fprintln(cmd.OutOrStdout(), "run cleanup recovered; key revoked")
					return nil
				}
			}
			if a.Worktree == "" || a.RunID == "" {
				if a.Worktree != "" {
					rec, err := runs.LoadFile(filepath.Join(a.KeyDir, "run.json"))
					if err == nil && rec.Ended != nil && rec.Key == "" {
						fmt.Fprintf(cmd.OutOrStdout(), "run %s already ended; key revoked\n", rec.ID)
						return nil
					}
				}
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
			// In-harness runs leave no log: the digest is the handover.
			end := server.EndRunRequest{Status: status, Digest: secrets.Digest{Summary: cleanSummary(rec, text)}}
			if err := endLease(a, rec, end); err != nil {
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

// leaseActor uses the permanent actor credential, which remains usable after
// a run key has been revoked. It may only inspect/end this actor's runs.
func leaseActor(a *agentCtx) (*agentCtx, error) {
	rec, err := runs.LoadFile(filepath.Join(a.KeyDir, "actor.json"))
	if err != nil {
		return nil, err
	}
	if rec.Key == "" || rec.Actor != a.Actor || rec.ServerURL != a.ServerURL {
		return nil, errors.New("actor credential does not match the lease")
	}
	actor := *a
	actor.Key, actor.RunID = rec.Key, ""
	return &actor, nil
}

func finishLease(a *agentCtx, rec runs.Record, ended secrets.Run) error {
	if ended.ID != rec.ID || ended.Actor != a.Actor || ended.Ended == nil {
		return errors.New("server did not confirm this run ended")
	}
	actor, err := leaseActor(a)
	if err != nil {
		return err
	}
	// Clear config first: if saving fails, run.json still identifies the run
	// for the next retry. resolveAgent selects records, not this config value.
	id, _ := git(a.Worktree, "config", "--worktree", "--get", gitRunKey)
	if id != "" && id != rec.ID {
		return fmt.Errorf("worktree now belongs to run %s; refusing cleanup of %s", id, rec.ID)
	}
	if _, err := git(a.Worktree, "config", "--worktree", gitRunKey, ""); err != nil {
		return err
	}
	rec.Status, rec.Ended, rec.Key = ended.Status, ended.Ended, ""
	if err := saveRecord(filepath.Join(a.KeyDir, "run.json"), rec); err != nil {
		return err
	}
	a.RunID, a.Key = "", actor.Key
	return nil
}

func endLease(a *agentCtx, rec runs.Record, end server.EndRunRequest) error {
	actor, err := leaseActor(a)
	if err != nil {
		return err
	}
	var ended secrets.Run
	if err := actor.call("DELETE", "/v1/runs/"+rec.ID, end, &ended); err != nil {
		return err
	}
	return finishLease(a, rec, ended)
}

// recoverLease reconciles only the durable local run ID, never a different
// active run. Startup interrupted before the prompt is delivered is revoked;
// an already ended run only needs its local metadata finalized.
func recoverLease(a *agentCtx) (bool, error) {
	file := filepath.Join(a.KeyDir, "run.json")
	rec, err := runs.LoadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rec.Key == "" {
		return false, nil
	}
	if rec.ID != a.RunID || rec.Actor != a.Actor || rec.Worktree != a.Worktree || rec.ServerURL != a.ServerURL {
		return false, errors.New("local lease identity mismatch; refusing recovery")
	}
	actor, err := leaseActor(a)
	if err != nil {
		return false, err
	}
	var remote secrets.Run
	if err := actor.call("GET", "/v1/runs/"+rec.ID, nil, &remote); err != nil {
		return false, err
	}
	if remote.ID != rec.ID || remote.Actor != rec.Actor {
		return false, errors.New("server lease identity mismatch; refusing recovery")
	}
	if remote.Ended != nil {
		return true, finishLease(a, rec, remote)
	}
	if rec.Status == runs.StatusPreparing {
		return true, endLease(a, rec, server.EndRunRequest{Status: "revoked"})
	}
	return false, nil
}

func prepareLease(a *agentCtx, rec runs.Record, setup func() error) (err error) {
	// Keep a durable identity before setup. A later start/end can revoke a
	// preparing run even if this process dies or rollback cannot reach the server.
	defer func() {
		if err != nil {
			cleanupErr := endLease(a, rec, server.EndRunRequest{Status: "revoked"})
			if cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback run %s (retry run end): %w", rec.ID, cleanupErr))
			}
		}
	}()
	if err := saveRecord(filepath.Join(a.KeyDir, "run.json"), rec); err != nil {
		return err
	}

	return setup()
}
