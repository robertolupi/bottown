package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/flow"
)

func newFlow(g *globals) *cobra.Command {
	var implement, verify, repo, message string
	var rounds, checkRounds int
	var timeout, checkTimeout time.Duration
	var waitActors bool
	cmd := &cobra.Command{
		Use:   "flow N --implement ACTOR[:MODEL] --verify ACTOR[:MODEL]",
		Short: "Drive one issue through implement and cross-family verify",
		Long: `Drive one issue through implement and cross-family verify runs, until its
pull request is ready to merge or the flow stops; rerunning the command
resumes from where the forge says the issue stands.

After each implement run, before a verifier sees a new head, the flow runs
the post-implement checks itself: the e2e commands .bottown.toml names on
the default branch (default: make e2e), in a clean worktree detached at the
head, with no agent involved. The log goes next to the run records
(.bottown/runs/flow-<issue>-<sha>.e2e.log), and the result is the commit
status bottown/e2e on the head, written by the owner. If they pass, the
verify run's prompt says so and names the log; if they fail, the flow
starts another implement round with the failure in its prompt. Those
rounds count against --check-rounds, not --rounds. A repository whose
.bottown.toml sets e2e = [] has no post-implement checks.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issue, err := parseNumber(args[0])
			if err != nil {
				return err
			}
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			if repo == "" {
				repo = rc.repo
			} else if rc, err = openRunContextRepo(g, repo); err != nil {
				return err
			}
			supervisor := &flow.Supervisor{
				API:     flow.NewClient(rc.inst, rc.key),
				Checker: flowChecker{ws: rc.ws},
				Out:     cmd.OutOrStdout(),
			}
			supervisor.Runner = flow.RunnerFunc(func(ctx context.Context, req flow.RunRequest) error {
				target := req.Issue
				if req.PR > 0 {
					target = req.PR
				}
				previous := cmd.Context()
				cmd.SetContext(ctx)
				err := startRun(cmd, g, req.Kind, fmt.Sprint(target), runOpts{actor: req.Actor, repo: req.Repo, message: message, bypassActorLimit: req.BypassActorLimit,
					flowNote: req.Note, checksPassed: req.ChecksPassed})
				cmd.SetContext(previous)
				if err != nil && strings.Contains(err.Error(), "live batch run") {
					return fmt.Errorf("%w: %v", flow.ErrActorLimit, err)
				}
				return err
			})
			result, err := supervisor.Run(cmd.Context(), flow.Options{
				Issue: issue, Repo: repo, Implement: implement, Verify: verify,
				Rounds: rounds, CheckRounds: checkRounds, Timeout: timeout, CheckTimeout: checkTimeout, WaitActors: waitActors,
			})
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "flow #%d stopped: %s\n", result.Issue, result.Reason)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&implement, "implement", "", "actor that implements the issue (required); ACTOR:MODEL chooses its harness's model, e.g. agy:gemini-3.8-flash")
	cmd.Flags().StringVar(&verify, "verify", "", "actor from another family that verifies the PR (required); ACTOR:MODEL as for --implement. The family is the actor's, whatever the model")
	cmd.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	cmd.Flags().IntVar(&rounds, "rounds", 3, "review rounds: the flow stops at this many requests for changes, so at most this many implement runs answer reviews, the first included")
	cmd.Flags().IntVar(&checkRounds, "check-rounds", 2, "implement runs the flow starts to fix failed post-implement checks; it stops when the checks fail again after these")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "time limit for each run")
	cmd.Flags().DurationVar(&checkTimeout, "check-timeout", time.Hour, "time limit for the post-implement checks at each head; running out is a failure")
	cmd.Flags().BoolVar(&waitActors, "wait-actors", false, "wait for each actor's live batch run slot instead of bypassing the limit")
	cmd.Flags().StringVarP(&message, "message", "m", "", `a note added to the goal prompt of every run the flow starts, implement and verify, e.g. "also read the epic #123"`)
	_ = cmd.MarkFlagRequired("implement")
	_ = cmd.MarkFlagRequired("verify")
	return cmd
}
