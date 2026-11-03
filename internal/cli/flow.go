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
	var rounds int
	var timeout time.Duration
	var waitActors bool
	cmd := &cobra.Command{
		Use:   "flow N --implement ACTOR[:MODEL] --verify ACTOR[:MODEL]",
		Short: "Drive one issue through implement and cross-family verify",
		Args:  cobra.ExactArgs(1),
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
			}
			supervisor := &flow.Supervisor{
				API: flow.NewClient(rc.inst, rc.key),
				Out: cmd.OutOrStdout(),
			}
			supervisor.Runner = flow.RunnerFunc(func(ctx context.Context, req flow.RunRequest) error {
				target := req.Issue
				if req.PR > 0 {
					target = req.PR
				}
				previous := cmd.Context()
				cmd.SetContext(ctx)
				err := startRun(cmd, g, req.Kind, fmt.Sprint(target), runOpts{actor: req.Actor, repo: req.Repo, message: message, bypassActorLimit: req.BypassActorLimit})
				cmd.SetContext(previous)
				if err != nil && strings.Contains(err.Error(), "live batch run") {
					return fmt.Errorf("%w: %v", flow.ErrActorLimit, err)
				}
				return err
			})
			result, err := supervisor.Run(cmd.Context(), flow.Options{
				Issue: issue, Repo: repo, Implement: implement, Verify: verify,
				Rounds: rounds, Timeout: timeout, WaitActors: waitActors,
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
	cmd.Flags().IntVar(&rounds, "rounds", 3, "maximum implement runs, including the first")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "time limit for each run")
	cmd.Flags().BoolVar(&waitActors, "wait-actors", false, "wait for each actor's live batch run slot instead of bypassing the limit")
	cmd.Flags().StringVarP(&message, "message", "m", "", `a note added to the goal prompt of every run the flow starts, implement and verify, e.g. "also read the epic #123"`)
	_ = cmd.MarkFlagRequired("implement")
	_ = cmd.MarkFlagRequired("verify")
	return cmd
}
