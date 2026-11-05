package cli

import (
	"context"
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
		Use:   "start implement|verify|discuss|plan [issue|pr] N",
		Short: "Start a run for this actor worktree and print its goal prompt (agent CLI)",
		Long: `Start a run for this actor worktree and print its goal prompt. The number may
be written 12, #12 or !12, optionally after "issue", "pr" or "pull request", so
slash commands can pass what the user typed: /bottown-implement issue 12.
Each argument is split on spaces, so it may also arrive as one quoted word.

A discuss run reviews and discusses an issue (or pull request) without
implementing it: the worktree is detached at the default branch, and the
run may read and comment, but not push, open pull requests or review.

A plan run writes a milestone plan from its kickoff issue on issue/<N>, like
an implement run, and opens a pull request for it; it cannot create issues.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
			if kind != policy.Implement && kind != policy.Verify && kind != policy.Discuss && kind != policy.Plan {
				return fmt.Errorf("kind must be %s, %s, %s or %s", policy.Implement, policy.Verify, policy.Discuss, policy.Plan)
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
	u, err := runs.Git(a.Worktree, nil, "remote", "get-url", a.Remote)
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
		if _, err := recoverLease(cmd.Context(), a); err != nil {
			return err
		}
	}
	switch {
	case a.Worktree == "":
		return errors.New("`bottown run start` works in an actor worktree; this is a launched run")
	case a.RunID != "":
		return fmt.Errorf("this worktree is already in run %s; finish it with `bottown run end`", a.RunID)
	}
	if st, err := runs.Git(a.Worktree, nil, "status", "--porcelain"); err != nil {
		return err
	} else if st != "" {
		return fmt.Errorf("worktree %s has uncommitted changes; commit, stash or clean them first", a.Worktree)
	}
	repo, err := worktreeRepo(a)
	if err != nil {
		return err
	}
	// Before the run exists: without the actor credential it could not be
	// rolled back or recovered.
	lc, err := leaseLifecycle(a)
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
	if err := a.call(cmd.Context(), "POST", "/v1/runs", req, &created); err != nil {
		return err
	}
	run := created.Run
	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: true, Harness: run.Harness, Started: time.Now().UTC(),
		Worktree: a.Worktree, Key: created.Key, ServerURL: a.ServerURL,
		Attachments: leaseAttachments(a),
	}
	if inst, _ := runs.Git(a.Worktree, nil, "config", "--get", gitInstanceKey); inst != "" {
		rec.Instance = inst
	}
	return lc.Prepare(cmd.Context(), rec, func() error {
		if _, err := runs.GitCtx(cmd.Context(), a.Worktree, nil, "fetch", "-q", "--prune", a.Remote); err != nil {
			return err
		}
		if kind == policy.Implement || kind == policy.Plan {
			upstream := run.Branch
			start := a.Remote + "/" + upstream
			if _, err := runs.Git(a.Worktree, nil, "rev-parse", "--verify", "-q", "refs/remotes/"+start); err != nil {
				top, _ := runs.Git(a.Worktree, nil, "rev-parse", "--show-toplevel")
				base, err := runs.RemoteDefaultBranch(cmd.Context(), top, a.Remote)
				if err != nil {
					return err
				}
				upstream = base
				start = a.Remote + "/" + upstream
			}
			if _, err := runs.Git(a.Worktree, nil, "checkout", "-q", "-B", run.Branch, start); err != nil {
				return err
			}
			for _, kv := range [][2]string{
				{"branch." + run.Branch + ".remote", a.Remote},
				{"branch." + run.Branch + ".merge", "refs/heads/" + upstream},
			} {
				if _, err := runs.Git(a.Worktree, nil, "config", kv[0], kv[1]); err != nil {
					return err
				}
			}
		} else {
			if _, err := runs.Git(a.Worktree, nil, "checkout", "-q", "--detach", run.SHA); err != nil {
				return err
			}
		}
		// After the checkout, which decides whether the repository tracks a
		// file in the guide's slot. Stdout is the goal prompt: nothing else.
		writeGuide(nil, cmd.ErrOrStderr(), a.Worktree)

		if _, err := runs.Git(a.Worktree, nil, "config", "--worktree", gitRunKey, run.ID); err != nil {
			return err
		}

		// The goal prompt, from the issue or PR text.
		rk := *a
		rk.Key = created.Key
		var item server.IssueOut
		if err := rk.call(cmd.Context(), "GET", fmt.Sprintf("/v1/forge/issue?number=%d", n), nil, &item); err != nil {
			return err
		}
		// A previous run's files, if its cleanup was interrupted, go first.
		if err := runs.RemoveAttachments(rec.Attachments); err != nil {
			return err
		}
		top, _ := runs.Git(a.Worktree, nil, "rev-parse", "--show-toplevel")
		base, _ := runs.RemoteDefaultBranch(cmd.Context(), top, a.Remote)
		d := runs.PromptData{Title: item.Title, Body: item.Body, Base: base, Bottown: selfPath(), Remote: a.Remote, SelfEnd: true}
		prompt, err := goalPrompt(cmd.Context(), &rk, run, d, a.Worktree, a.Remote+"/"+base, rec.Attachments, &item, nil)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(a.KeyDir, "prompt.md"), []byte(prompt), 0o600); err != nil {
			return err
		}
		if err := lc.Running(nil); err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), prompt)
		return err
	})
}

// leaseAttachments is the attachments directory of an actor worktree's
// runs: in its git dir, next to run.json and prompt.md, outside the
// checkout so a `git add -A` cannot commit the files.
func leaseAttachments(a *agentCtx) string { return filepath.Join(a.KeyDir, "attachments") }

// leaseStore is the actor worktree's run record: its latest run, live or
// ended.
func leaseStore(a *agentCtx) runs.Store {
	return runs.Store{Path: filepath.Join(a.KeyDir, "run.json")}
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
			if a.Worktree == "" && a.RunFile != "" {
				return handOverLaunched(cmd, a, failed, summary, summaryFile)
			}
			if a.Worktree != "" {
				recovered, err := recoverLease(cmd.Context(), a)
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
					rec, err := leaseStore(a).Load()
					if err == nil && rec.Ended != nil && rec.Key == "" {
						fmt.Fprintf(cmd.OutOrStdout(), "run %s already ended; key revoked\n", rec.ID)
						return nil
					}
				}
				return errors.New("no run to end in this worktree")
			}
			rec, err := leaseStore(a).Load()
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
			if err := a.call(cmd.Context(), "POST", "/v1/forge/comment", server.CommentIn{Body: handoverText(rec, status, 0, text)}, nil); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: handover comment failed: %v\n", err)
			}
			// In-harness runs leave no log: the digest is the handover.
			end := server.EndRunRequest{Status: status, Digest: secrets.Digest{Summary: cleanSummary(rec, text)}}
			if failed {
				// What is observed is only that the agent said so; why is
				// its summary.
				end.Failure = &secrets.Failure{Cause: secrets.FailureReported, Detail: "the agent ended the run as failed"}
			}
			if err := endLease(cmd.Context(), a, rec, end); err != nil {
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

// handOverLaunched is `run end` in a launched run ($BOTTOWN_RUN_FILE): the
// launcher owns the run, and ends it when the harness exits, with the run
// key it still needs for the handover. So the agent's summary, and whether
// it failed, go in the run's record, for the launcher's handover and digest.
func handOverLaunched(cmd *cobra.Command, a *agentCtx, failed bool, summary, summaryFile string) error {
	text, err := readBody(cmd, summary, summaryFile)
	if err != nil {
		return err
	}
	if err := (runs.Store{Path: a.RunFile}).Update(func(r *runs.Record) error {
		if r.ID != a.RunID || r.Key == "" {
			return fmt.Errorf("run %s has ended", a.RunID)
		}
		r.Handover = &runs.Handover{Summary: strings.TrimSpace(text), Failed: failed}
		return nil
	}); err != nil {
		return err
	}
	status := runs.StatusSucceeded
	if failed {
		status = runs.StatusFailed
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run %s: handover saved (%s); the launcher posts it and ends the run when you exit\n", a.RunID, status)
	return nil
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

// leaseLifecycle is the lifecycle of the worktree's runs. It reaches the
// server with the actor credential, so it can end or inspect a run whose
// key is already revoked. Once it has ended a run, a acts with that
// credential too.
func leaseLifecycle(a *agentCtx) (*runs.Lifecycle, error) {
	actor, err := leaseActor(a)
	if err != nil {
		return nil, err
	}
	return &runs.Lifecycle{
		Mode:  runs.Leased,
		Store: leaseStore(a),
		EndRun: func(ctx context.Context, id, status string, d secrets.Digest) (secrets.Run, error) {
			var ended secrets.Run
			err := actor.call(ctx, "DELETE", "/v1/runs/"+id, server.EndRunRequest{Status: status, Digest: d}, &ended)
			return ended, err
		},
		GetRun: func(ctx context.Context, id string) (secrets.Run, error) {
			var remote secrets.Run
			err := actor.call(ctx, "GET", "/v1/runs/"+id, nil, &remote)
			return remote, err
		},
		Release: func(rec runs.Record) error {
			// Clear config first: if saving fails, run.json still identifies
			// the run for the next retry. resolveAgent selects records, not
			// this config value.
			id, _ := runs.Git(a.Worktree, nil, "config", "--worktree", "--get", gitRunKey)
			if id != "" && id != rec.ID {
				return fmt.Errorf("worktree now belongs to run %s; refusing cleanup of %s", id, rec.ID)
			}
			if _, err := runs.Git(a.Worktree, nil, "config", "--worktree", gitRunKey, ""); err != nil {
				return err
			}
			// The run's attachments end with it.
			return runs.RemoveAttachments(leaseAttachments(a))
		},
		Ended: func(runs.Record) { a.RunID, a.Key = "", actor.Key },
	}, nil
}

// endLease ends the worktree's run rec, on the server and then locally.
func endLease(ctx context.Context, a *agentCtx, rec runs.Record, end server.EndRunRequest) error {
	lc, err := leaseLifecycle(a)
	if err != nil {
		return err
	}
	lc.Resume(rec)
	return lc.End(ctx, end.Status, end.Digest)
}

// recoverLease reconciles only the durable local run ID, never a different
// active run. Startup interrupted before the prompt is delivered is revoked;
// an already ended run only needs its local metadata finalized.
func recoverLease(ctx context.Context, a *agentCtx) (bool, error) {
	rec, err := leaseStore(a).Load()
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
	lc, err := leaseLifecycle(a)
	if err != nil {
		return false, err
	}
	return lc.Recover(ctx, rec)
}
