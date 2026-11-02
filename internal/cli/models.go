package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

func newModels(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models [ACTOR [ARGS...]]",
		Short: "List the models an actor's harness accepts as the SPEC of ACTOR:SPEC",
		Long: `List the models an actor's harness accepts as the SPEC of ACTOR:SPEC
(run -a ACTOR:SPEC, flow --implement/--verify ACTOR:SPEC), by running the
harness's own listing: agy models for antigravity, crush models for crush.
ARGS after ACTOR go to that command unchanged (crush models openrouter, say),
so bottown's own flags come before ACTOR. Without ACTOR, it lists the models
of every actor on the instance. A harness with no listing command (claude,
codex, kimi) is named as such: give it a model name its own model flag takes.

For crush, crush:PROVIDER:MODEL and crush:PROVIDER/MODEL both choose MODEL
from PROVIDER.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			key, err := ownerKey(g)
			if err != nil {
				return err
			}
			var actors []server.ActorInfo
			if err := callServer(cmd.Context(), inst, key, "GET", "/v1/actors", nil, &actors); err != nil {
				return err
			}
			return listModels(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), actors, args)
		},
	}
	// Flags after ACTOR belong to the harness's listing.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// listModels runs the listing of args[0]'s harness with args[1:], or, with
// no args, of every actor's harness in turn, under a heading each.
func listModels(ctx context.Context, out, errOut io.Writer, actors []server.ActorInfo, args []string) error {
	if len(args) > 0 {
		i := slices.IndexFunc(actors, func(a server.ActorInfo) bool { return a.Name == args[0] })
		if i < 0 {
			names := []string{}
			for _, a := range actors {
				names = append(names, a.Name)
			}
			return fmt.Errorf("%q is not an actor (actors: %s)", args[0], strings.Join(names, ", "))
		}
		return actorModels(ctx, out, errOut, actors[i], args[1:])
	}
	var errs []error
	for i, a := range actors {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "== %s (%s)\n", a.Name, harnessOf(a))
		if err := actorModels(ctx, out, errOut, a, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// actorModels passes through to a's harness listing, or says it has none.
func actorModels(ctx context.Context, out, errOut io.Writer, a server.ActorInfo, args []string) error {
	harness := harnessOf(a)
	h, err := runs.Binding(harness)
	if err != nil {
		return fmt.Errorf("%s: %w", a.Name, err)
	}
	l, ok := h.(runs.ModelLister)
	if !ok {
		if len(args) > 0 {
			return fmt.Errorf("%s: harness %s has no command that lists its models", a.Name, harness)
		}
		fmt.Fprintf(out, "harness %s has no command that lists its models: give %s:MODEL a name its own model flag takes\n", harness, a.Name)
		return nil
	}
	c := l.ModelsCommand(ctx, args)
	c.Stdout, c.Stderr = out, errOut
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %s: %w", a.Name, strings.Join(c.Args, " "), err)
	}
	return nil
}

// harnessOf is the harness that runs a, which defaults to its name (as in
// run implement).
func harnessOf(a server.ActorInfo) string {
	if a.Harness == "" {
		return a.Name
	}
	return a.Harness
}
