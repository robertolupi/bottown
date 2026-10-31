package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

type runOpts struct {
	actor           string
	interactive     bool
	allowSameFamily bool
	allow           []string
	keep            bool
}

func newRun(g *globals) *cobra.Command {
	run := &cobra.Command{
		Use:   "run",
		Short: "Run agents on this checkout's issues and pull requests",
		Long: `Run an agent as an actor, in a role, on one issue or pull request of the
hosted checkout you are in. Each run gets a fresh worktree under .bottown/runs/,
the actor's git identity, and a key limited to the role and this repository.`,
	}
	var o runOpts
	implement := &cobra.Command{
		Use:   "implement -a ACTOR [-I] ISSUE",
		Short: "Implement an issue: the agent pushes a branch and opens a pull request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return startRun(cmd, g, policy.Implement, args[0], o)
		},
	}
	verify := &cobra.Command{
		Use:   "verify -a ACTOR [-I] [--allow-same-family] PR",
		Short: "Verify a pull request at its current head: the agent submits one review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return startRun(cmd, g, policy.Verify, args[0], o)
		},
	}
	for _, c := range []*cobra.Command{implement, verify} {
		c.Flags().StringVarP(&o.actor, "actor", "a", "", "actor to run as (required), e.g. claude, codex")
		c.Flags().BoolVarP(&o.interactive, "interactive", "I", false, "run the harness's interactive UI in this terminal, so you can steer it")
		c.Flags().StringSliceVar(&o.allow, "allow", nil, "extra tools the agent may use without asking (harness syntax, e.g. 'Bash(cargo:*)')")
		c.Flags().BoolVar(&o.keep, "keep", false, "keep the worktree even if the run succeeds")
		_ = c.MarkFlagRequired("actor")
	}
	verify.Flags().BoolVar(&o.allowSameFamily, "allow-same-family", false, "allow a verifier of the author's model family (recorded on the review)")

	run.AddCommand(implement, verify, newRunLs(g), newRunShow(g), newRunGC(g))
	run.AddCommand(agentCmds(newRunStart(), newRunEnd())...)
	return run
}

// runContext is the hosted checkout a run command operates on.
type runContext struct {
	inst config.Instance
	ws   *runs.Workspace
	org  string
	repo string // name within org
	key  string // owner API key
}

func openRunContext(g *globals) (*runContext, error) {
	inst, err := g.loadInstance()
	if err != nil {
		return nil, err
	}
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, errors.New("not inside a git checkout")
	}
	if strings.Contains(filepath.ToSlash(top), "/.bottown/runs/") {
		return nil, errors.New("this is a run worktree; run bottown from the hosted checkout")
	}
	full, ok := repoOnInstance(top, inst)
	if !ok {
		return nil, fmt.Errorf("this checkout is not hosted on instance %s; run `bottown -i %s repo add` first", inst.Name, inst.Name)
	}
	org, name, _ := strings.Cut(full, "/")
	key, err := ownerKey(g)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	auth := []string{"-c", "credential.helper=", "-c", fmt.Sprintf("credential.helper=!%q -i %s credential", self, inst.Name)}
	ws, err := runs.Open(top, inst.ServerURL()+"/git/"+full+".git", auth)
	if err != nil {
		return nil, err
	}
	return &runContext{inst: inst, ws: ws, org: org, repo: name, key: key}, nil
}

func parseNumber(s string) (int64, error) {
	s = strings.TrimLeft(s, "#!")
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not an issue or pull request number", s)
	}
	return n, nil
}

func startRun(cmd *cobra.Command, g *globals, kind, target string, o runOpts) (err error) {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	n, err := parseNumber(target)
	if err != nil {
		return err
	}
	rc, err := openRunContext(g)
	if err != nil {
		return err
	}
	if err := rc.ws.Sync(ctx); err != nil {
		return err
	}

	req := server.RunRequest{Kind: kind, Actor: o.actor, Repo: rc.repo, AllowSameFamily: o.allowSameFamily, Interactive: o.interactive}
	if kind == policy.Implement {
		req.Issue = n
	} else {
		req.PR = n
	}
	var created server.RunResponse
	if err := callServer(ctx, rc.inst, rc.key, "POST", "/v1/runs", req, &created); err != nil {
		return err
	}
	run := created.Run
	// From here on, always end the run on the server, whatever happens.
	status := runs.StatusFailed
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := callServer(cctx, rc.inst, rc.key, "DELETE", "/v1/runs/"+run.ID, map[string]string{"status": status}, nil); e != nil && err == nil {
			err = fmt.Errorf("end run %s: %w", run.ID, e)
		}
	}()

	prompt, base, err := renderPrompt(ctx, rc, run)
	if err != nil {
		return err
	}
	var dir string
	if kind == policy.Implement {
		dir, err = rc.ws.AddImplement(run.ID, run.Branch, base)
	} else {
		dir, err = rc.ws.AddVerify(run.ID, run.SHA)
	}
	if err != nil {
		return err
	}
	harnessName := run.Harness
	if harnessName == "" {
		harnessName = run.Actor
	}
	h, err := runs.LookupHarness(harnessName)
	if err != nil {
		return err
	}

	recPath := rc.ws.Path(run.ID, ".json")
	self, _ := os.Executable()
	if err := runs.Configure(dir, run.Actor, fmt.Sprintf("!%q credential --run-file %q", self, recPath)); err != nil {
		return err
	}
	mcpURL := rc.inst.ServerURL() + "/mcp"
	if err := os.WriteFile(rc.ws.Path(run.ID, ".prompt.md"), []byte(prompt), 0o600); err != nil {
		return err
	}
	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family, Instance: rc.inst.Name,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: o.interactive, AllowSameFamily: run.AllowSameFamily, Harness: harnessName,
		Status: runs.StatusRunning, PID: os.Getpid(), Started: time.Now().UTC(), Worktree: dir,
		Key: created.Key, ServerURL: rc.inst.ServerURL(),
	}
	if err := rc.ws.Save(rec); err != nil {
		return err
	}

	l := runs.Launch{
		Dir: dir, Prompt: prompt, PromptFile: rc.ws.Path(run.ID, ".prompt.md"),
		MCPURL: mcpURL, Key: created.Key,
		RecordFile: recPath, RunID: run.ID, Interactive: o.interactive, Allow: o.allow,
	}
	if err := h.PrepareMCP(&l, rc.ws.RunsDir); err != nil {
		return err
	}
	// The MCP config holds the run key: delete it however the run ends.
	defer os.Remove(l.MCPConfig)
	c, err := h.Command(ctx, l)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "run %s: %s %s as %s (%s, %s) in %s\n", run.ID, kind, rec.Target(), run.Actor, run.Family, rec.Mode(), dir)

	logf, err := os.OpenFile(rc.ws.Path(run.ID, ".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if o.interactive {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		c.Stdout, c.Stderr = logf, logf
		fmt.Fprintf(out, "  log: %s\n", rc.ws.Path(run.ID, ".log"))
	}
	runErr := c.Run()
	code := 0
	if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
	}
	if runErr != nil && code == 0 {
		code = -1
	}
	status = runs.StatusFailed
	if code == 0 {
		status = runs.StatusSucceeded
	}
	if o.interactive {
		status = askOutcome(cmd, status)
	}

	logBytes, _ := os.ReadFile(rc.ws.Path(run.ID, ".log"))
	if err := handover(ctx, rc, run, rec, created.Key, status, code, h.Summary(logBytes)); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: handover comment failed: %v\n", err)
	}

	now := time.Now().UTC()
	rec.Status, rec.ExitCode, rec.Ended, rec.Key = status, &code, &now, ""
	if err := rc.ws.Save(rec); err != nil {
		return err
	}
	st := newStyles(lipgloss.NewRenderer(out))
	if status == runs.StatusSucceeded && !o.keep {
		if err := rc.ws.Remove(run.ID); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: remove worktree: %v\n", err)
		}
		fmt.Fprintf(out, "run %s %s (worktree removed)\n", st.Info.Bold(true).Render(run.ID), st.runStatus(status))
	} else {
		fmt.Fprintf(out, "run %s %s (exit %d); worktree kept at %s\n", st.Info.Bold(true).Render(run.ID), st.runStatus(status), code, dir)
	}
	if status != runs.StatusSucceeded {
		return fmt.Errorf("run %s %s", run.ID, status)
	}
	return nil
}

// renderPrompt fetches the issue or PR and renders the goal prompt. It
// also returns the repository's default branch.
func renderPrompt(ctx context.Context, rc *runContext, run secrets.Run) (string, string, error) {
	base, err := rc.ws.DefaultBranch(ctx)
	if err != nil {
		return "", "", err
	}
	n := run.Issue
	if run.Kind == policy.Verify {
		n = run.PR
	}
	var item struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := callServer(ctx, rc.inst, rc.key, "GET", fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d", run.Org, run.Repo, n), nil, &item); err != nil {
		return "", "", err
	}
	p, err := runs.Prompt(run.Kind, runs.PromptData{
		RunID: run.ID, Actor: run.Actor, Family: run.Family, Repo: run.Org + "/" + run.Repo,
		Issue: run.Issue, PR: run.PR, Title: item.Title, Body: item.Body,
		Branch: run.Branch, Base: base, SHA: run.SHA, Author: run.Author, AllowSameFamily: run.AllowSameFamily,
		Bottown: selfPath(), Remote: "origin",
	})
	return p, base, err
}

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "bottown"
	}
	return p
}

func askOutcome(cmd *cobra.Command, def string) string {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return def
	}
	hint := "y/N"
	if def == runs.StatusSucceeded {
		hint = "Y/n"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Did the run succeed? [%s] ", hint)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return runs.StatusSucceeded
	case "n", "no":
		return runs.StatusFailed
	}
	return def
}

// handover posts the run's outcome on its issue (implement, discuss) or PR (verify),
// with the run key, as the actor.
func handover(ctx context.Context, rc *runContext, run secrets.Run, rec runs.Record, key, status string, code int, summary string) error {
	n := run.Issue
	if run.Kind == policy.Verify {
		n = run.PR
	}
	path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", run.Org, run.Repo, n)
	return callServer(ctx, rc.inst, key, "POST", path, map[string]string{"body": handoverText(rec, status, code, summary)}, nil)
}

// handoverText is the handover comment for a finished run.
func handoverText(rec runs.Record, status string, code int, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**bottown run `%s`** · %s · %s (%s) · %s · **%s**", rec.ID, rec.Kind, rec.Actor, rec.Family, rec.Mode(), status)
	if code != 0 {
		fmt.Fprintf(&b, " (exit %d)", code)
	}
	b.WriteString("\n\n")
	switch rec.Kind {
	case policy.Implement:
		if head, err := runs.Git(rec.Worktree, nil, "rev-parse", "--short", "HEAD"); err == nil {
			fmt.Fprintf(&b, "Branch `%s` at `%s`.\n\n", rec.Branch, head)
		}
	case policy.Discuss:
		fmt.Fprintf(&b, "Discussed against `%s`, the default branch's head.\n\n", rec.SHA)
	default:
		fmt.Fprintf(&b, "Verified at `%s`.\n\n", rec.SHA)
	}
	if rec.Interactive {
		b.WriteString("_Interactive run: the owner could steer the agent, so this result is partly the owner's work._\n\n")
	}
	// Agents link files by absolute local path; on the forge, repo-relative
	// paths are what make sense.
	summary = strings.ReplaceAll(summary, "file://"+rec.Worktree+"/", "")
	summary = strings.ReplaceAll(summary, rec.Worktree+"/", "")
	if s := strings.TrimSpace(summary); s != "" {
		const max = 4000
		if len(s) > max {
			s = s[:max] + "…"
		}
		b.WriteString(s + "\n")
	}
	return b.String()
}

func newRunLs(g *globals) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List this checkout's runs (local worktrees merged with the server's records)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if all {
				return lsAll(cmd, g)
			}
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			local, err := rc.ws.Records()
			if err != nil {
				return err
			}
			var remote []secrets.Run
			if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs?repo="+rc.repo, nil, &remote); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			return printRunLs(out, lipgloss.NewRenderer(out), local, remote)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list the server's runs for every repository on the instance")
	return cmd
}

func printRunLs(out io.Writer, r *lipgloss.Renderer, local []runs.Record, remote []secrets.Run) error {
	byID := map[string]secrets.Run{}
	for _, rem := range remote {
		byID[rem.ID] = rem
	}
	st := newStyles(r)
	tw := newTableWriter(out)
	tw.Row(
		st.Header.Render("RUN"),
		st.Header.Render("KIND"),
		st.Header.Render("TARGET"),
		st.Header.Render("ACTOR"),
		st.Header.Render("FAMILY"),
		st.Header.Render("MODE"),
		st.Header.Render("STATUS"),
		st.Header.Render("KEY"),
		st.Header.Render("BRANCH/SHA"),
		st.Header.Render("AGE"),
		st.Header.Render("SIZE"),
	)
	seen := map[string]bool{}
	for _, rec := range local {
		seen[rec.ID] = true
		keySt := "gone"
		if sr, ok := byID[rec.ID]; ok {
			keySt = serverKeyState(sr)
		}
		size := "-"
		if _, err := os.Stat(rec.Worktree); err == nil {
			size = humanBytes(runs.DirSize(rec.Worktree))
		}
		tw.Row(
			st.Info.Bold(true).Render(rec.ID),
			st.Purple.Render(rec.Kind),
			st.Bold.Render(rec.Target()),
			st.Info.Render(rec.Actor),
			rec.Family,
			rec.Mode(),
			st.runStatus(rec.Status),
			st.keyState(keySt),
			branchOrSHA(rec.Branch, rec.SHA),
			st.Dim.Render(age(rec.Started)),
			size,
		)
	}
	for _, sr := range remote {
		if seen[sr.ID] {
			continue
		}
		if sr.Ended != nil {
			continue
		}
		mode := "batch"
		if sr.Interactive {
			mode = "interactive"
		}
		target := fmt.Sprintf("#%d", sr.Issue)
		if sr.PR > 0 {
			target = fmt.Sprintf("!%d", sr.PR)
		}
		status := sr.Status
		if status == "" {
			if serverKeyState(sr) == "live" {
				status = runs.StatusRunning
			} else {
				status = runs.StatusDead
			}
		}
		tw.Row(
			st.Info.Bold(true).Render(sr.ID),
			st.Purple.Render(sr.Kind),
			st.Bold.Render(target),
			st.Info.Render(sr.Actor),
			sr.Family,
			mode,
			st.runStatus(status),
			st.keyState(serverKeyState(sr)),
			branchOrSHA(sr.Branch, sr.SHA),
			st.Dim.Render(age(sr.Created)),
			"-",
		)
	}
	return tw.Flush()
}

func lsAll(cmd *cobra.Command, g *globals) error {
	inst, err := g.loadInstance()
	if err != nil {
		return err
	}
	key, err := ownerKey(g)
	if err != nil {
		return err
	}
	var remote []secrets.Run
	if err := callServer(cmd.Context(), inst, key, "GET", "/v1/runs", nil, &remote); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	return printRunLsAll(out, lipgloss.NewRenderer(out), remote)
}

func printRunLsAll(out io.Writer, r *lipgloss.Renderer, remote []secrets.Run) error {
	st := newStyles(r)
	tw := newTableWriter(out)
	tw.Row(
		st.Header.Render("RUN"),
		st.Header.Render("REPO"),
		st.Header.Render("KIND"),
		st.Header.Render("TARGET"),
		st.Header.Render("ACTOR"),
		st.Header.Render("FAMILY"),
		st.Header.Render("KEY"),
		st.Header.Render("STATUS"),
		st.Header.Render("AGE"),
	)
	for _, sr := range remote {
		target := fmt.Sprintf("#%d", sr.Issue)
		if sr.PR > 0 {
			target = fmt.Sprintf("!%d", sr.PR)
		}
		status := sr.Status
		if status == "" {
			if serverKeyState(sr) == "live" {
				status = runs.StatusRunning
			} else {
				status = runs.StatusDead
			}
		}
		tw.Row(
			st.Info.Bold(true).Render(sr.ID),
			st.Bold.Render(sr.Repo),
			st.Purple.Render(sr.Kind),
			st.Bold.Render(target),
			st.Info.Render(sr.Actor),
			sr.Family,
			st.keyState(serverKeyState(sr)),
			st.runStatus(status),
			st.Dim.Render(age(sr.Created)),
		)
	}
	return tw.Flush()
}

func serverKeyState(r secrets.Run) string {
	switch {
	case r.Ended != nil:
		return "ended"
	case time.Now().After(r.Expires):
		return "expired"
	}
	return "live"
}

func branchOrSHA(branch, sha string) string {
	if sha != "" {
		return sha[:min(len(sha), 10)] + " (detached)"
	}
	return branch
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func newRunShow(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "show RUN",
		Aliases: []string{"status"},
		Short:   "Show a run's local record, server record and log tail",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var (
				rec        *runs.Record
				sr         *secrets.Run
				srErr      error
				logContent string
			)
			if r, err := rc.ws.Load(args[0]); err == nil {
				r.Key = "" // never print a live key
				rec = &r
			}
			var s secrets.Run
			if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs/"+args[0], nil, &s); err == nil {
				sr = &s
			} else {
				srErr = err
			}
			logPath := rc.ws.Path(args[0], ".log")
			if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
				logContent = string(b)
			}
			printRunShow(out, lipgloss.NewRenderer(out), rec, sr, srErr, logPath, logContent)
			return nil
		},
	}
}

func printRunShow(out io.Writer, r *lipgloss.Renderer, rec *runs.Record, sr *secrets.Run, srErr error, logPath, logContent string) {
	st := newStyles(r)
	if rec != nil {
		fmt.Fprintf(out, "%s  %s %s %s as %s (%s), %s, %s\n",
			st.Bold.Render("local:"),
			st.Info.Bold(true).Render(rec.ID),
			st.Purple.Render(rec.Kind),
			st.Bold.Render(rec.Target()),
			st.Info.Render(rec.Actor),
			rec.Family,
			rec.Mode(),
			st.runStatus(rec.Status),
		)
		fmt.Fprintf(out, "        worktree %s\n        started %s",
			rec.Worktree,
			st.Dim.Render(rec.Started.Local().Format(time.DateTime)),
		)
		if rec.Ended != nil {
			fmt.Fprintf(out, ", ended %s", st.Dim.Render(rec.Ended.Local().Format(time.DateTime)))
		}
		fmt.Fprintln(out)
	} else {
		fmt.Fprintf(out, "%s  no record\n", st.Bold.Render("local:"))
	}
	if sr != nil {
		fmt.Fprintf(out, "%s key %s, expires %s",
			st.Bold.Render("server:"),
			st.keyState(serverKeyState(*sr)),
			st.Dim.Render(sr.Expires.Local().Format(time.DateTime)),
		)
		if sr.Status != "" {
			fmt.Fprintf(out, ", ended %s", st.runStatus(sr.Status))
		}
		if sr.AllowSameFamily {
			fmt.Fprint(out, ", same-family override")
		}
		fmt.Fprintln(out)
	} else if srErr != nil {
		fmt.Fprintf(out, "%s %v\n", st.Bold.Render("server:"), srErr)
	}
	if len(logContent) > 0 {
		lines := strings.Split(strings.TrimRight(logContent, "\n"), "\n")
		fmt.Fprintf(out, "%s (%s, last %d lines):\n",
			st.Bold.Render("log"),
			logPath,
			min(len(lines), 20),
		)
		for _, l := range lines[max(0, len(lines)-20):] {
			fmt.Fprintf(out, "  %s\n", truncate(l, 200))
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newRunGC(g *globals) *cobra.Command {
	var failed bool
	var olderThan time.Duration
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove finished runs (succeeded and dead; with --failed also failed ones)",
		Long: `Remove finished runs: their local worktrees and files (succeeded and dead;
with --failed also failed ones), then the server's records of this
repository's ended runs, including those started in actor worktrees. A run
whose local files are kept keeps its server record too. Runs that have not
ended, live or expired, are never purged; --older-than keeps recently ended
ones.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			recs, err := rc.ws.Records()
			if err != nil {
				return err
			}
			removed := 0
			var keep []string // local runs gc leaves, whose server record stays
			for _, r := range recs {
				switch r.Status {
				case runs.StatusRunning:
					keep = append(keep, r.ID)
					continue
				case runs.StatusFailed:
					if !failed {
						keep = append(keep, r.ID)
						continue
					}
				case runs.StatusDead:
					// Its launcher died: make sure the key is gone too.
					_ = callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+r.ID, map[string]string{"status": "revoked"}, nil)
				}
				if err := rc.ws.Remove(r.ID); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %v\n", r.ID, err)
					keep = append(keep, r.ID)
					continue
				}
				rc.ws.RemoveFiles(r.ID)
				removed++
			}
			_, _ = runs.Git(rc.ws.Mirror, nil, "worktree", "prune")
			var purged struct {
				Purged []string `json:"purged"`
			}
			req := server.PurgeRequest{Repo: rc.repo, EndedBefore: time.Now().Add(-olderThan), Keep: keep}
			if err := callServer(cmd.Context(), rc.inst, rc.key, "POST", "/v1/runs/purge", req, &purged); err != nil {
				return fmt.Errorf("removed %d runs; purging server records: %w", removed, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %d runs, purged %d ended runs from the server; .bottown is %s\n",
				removed, len(purged.Purged), humanBytes(runs.DirSize(rc.ws.Dir)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&failed, "failed", false, "also remove failed runs (kept by default for inspection)")
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "purge only server records of runs that ended at least this long ago (e.g. 168h)")
	return cmd
}

// mcpCall is `bottown mcp call TOOL [JSON]`: calls a bottown MCP tool with
// $BOTTOWN_RUN_KEY (inside a run) or the owner's key. For debugging, and for
// harnesses or scripts without MCP support.
func mcpCall(cmd *cobra.Command, g *globals, tool, args string) error {
	url, key := os.Getenv("BOTTOWN_MCP_URL"), os.Getenv("BOTTOWN_RUN_KEY")
	if url == "" || key == "" {
		inst, err := g.loadInstance()
		if err != nil {
			return err
		}
		if key, err = ownerKey(g); err != nil {
			return err
		}
		url = inst.ServerURL() + "/mcp"
	}
	return runs.CallTool(cmd.Context(), url, key, tool, args, cmd.OutOrStdout())
}

func newMCP(g *globals) *cobra.Command {
	m := &cobra.Command{Use: "mcp", Short: "bottown MCP utilities"}
	m.AddCommand(&cobra.Command{
		Use:   "call TOOL [JSON-ARGS]",
		Short: "Call a bottown MCP tool (uses $BOTTOWN_RUN_KEY inside a run)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := ""
			if len(args) == 2 {
				a = args[1]
			}
			return mcpCall(cmd, g, args[0], a)
		},
	})
	return m
}
