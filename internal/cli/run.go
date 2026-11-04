package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
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
	actor            string
	repo             string
	interactive      bool
	allowSameFamily  bool
	bypassActorLimit bool
	allow            []string
	keep             bool
	question         string // discuss: the owner's question for the prompt
	message          string // implement, verify, plan: the owner's note for the prompt
	flowNote         string // implement, verify: bottown flow's note (the post-implement checks)
	checksPassed     bool   // verify: the flow's post-implement checks passed at the head
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
	var questionFile string
	discuss := &cobra.Command{
		Use:   "discuss -a ACTOR [-I] N [QUESTION... | --question-file F|-]",
		Short: "Discuss an issue or pull request without implementing it: the agent answers in a comment",
		Long: `Discuss an issue or pull request without implementing it. The agent reads the
issue and the code at the default branch's head, and answers in one comment;
the run cannot push, open pull requests or review.

The question can follow the number, but your shell reads it first: quote it,
since ?, *, #, ' and ! mean something to the shell, and put -- before it if a
word starts with -. --question-file - reads it from stdin, which the shell
leaves alone. Without a question, the agent reviews the issue.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := discussQuestion(cmd, args[1:], questionFile)
			if err != nil {
				return err
			}
			o.question = q
			return startRun(cmd, g, policy.Discuss, args[0], o)
		},
	}
	discuss.Flags().StringVar(&questionFile, "question-file", "", "read the question from a file, or - for stdin")
	for _, c := range []*cobra.Command{implement, verify, discuss} {
		runFlags(c, &o)
	}
	for _, c := range []*cobra.Command{implement, verify} {
		messageFlag(c, &o.message)
	}
	verify.Flags().BoolVar(&o.allowSameFamily, "allow-same-family", false, "allow a verifier of the author's model family (recorded on the review)")

	run.AddCommand(implement, verify, discuss, newRunPlan(g), newRunLs(g), newRunShow(g), newRunStop(g), newRunGC(g))
	run.AddCommand(agentCmds(newRunStart(), newRunEnd())...)
	return run
}

// runFlags are the flags of the commands that launch a run.
func runFlags(c *cobra.Command, o *runOpts) {
	c.Flags().StringVarP(&o.actor, "actor", "a", "", "actor to run as (required), e.g. claude, codex, or ACTOR:MODEL to choose its harness's model, e.g. agy:gemini-3.8-flash, crush:openrouter:~deepseek/latest")
	c.Flags().BoolVarP(&o.interactive, "interactive", "I", false, "run the harness's interactive UI in this terminal, so you can steer it")
	c.Flags().StringSliceVar(&o.allow, "allow", nil, "extra tools the agent may use without asking (harness syntax, e.g. 'Bash(cargo:*)')")
	c.Flags().BoolVar(&o.keep, "keep", false, "keep the worktree even if the run succeeds")
	_ = c.MarkFlagRequired("actor")
}

// messageFlag is --message, the owner's note added to a run's goal prompt.
func messageFlag(c *cobra.Command, message *string) {
	c.Flags().StringVarP(message, "message", "m", "", `a note added to the agent's goal prompt, e.g. "also read the epic #123"`)
}

// newRunPlan is `run plan`, which `milestone plan` also is.
func newRunPlan(g *globals) *cobra.Command {
	var o runOpts
	c := &cobra.Command{
		Use:   "plan -a ACTOR [-I] ISSUE",
		Short: "Plan a milestone from its kickoff issue: the agent opens a pull request with the plan file",
		Long: `Plan a milestone from its kickoff issue. The agent reads the issue and the
design it references, writes <milestones_dir>/<slug>.md, checks it with
` + "`bottown milestone check`" + `, pushes issue/<ISSUE> and opens a pull request. The run
cannot create issues: once the plan is reviewed and merged, the owner's
` + "`bottown milestone apply`" + ` files them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return startRun(cmd, g, policy.Plan, args[0], o)
		},
	}
	runFlags(c, &o)
	messageFlag(c, &o.message)
	return c
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
	return openRunContextRepo(g, "")
}

func openRunContextRepo(g *globals, repoOverride string) (*runContext, error) {
	top, inst, org, name, key, err := ownerRepo(g, repoOverride)
	if err != nil {
		return nil, err
	}
	full := org + "/" + name
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	auth := []string{"-c", "credential.helper=", "-c", fmt.Sprintf("credential.helper=!%q -i %s credential", self, inst.Name)}
	ws, err := runs.Open(top, inst.Remote(), inst.ServerURL()+"/git/"+full+".git", auth)
	if err != nil {
		return nil, err
	}
	return &runContext{inst: inst, ws: ws, org: org, repo: name, key: key}, nil
}

// ownerRepo resolves the main checkout an owner command runs in, its
// instance, the repository (org and name: the checkout's, or repoOverride)
// and the owner's API key.
func ownerRepo(g *globals, repoOverride string) (top string, inst config.Instance, org, name, key string, err error) {
	if top, err = mainCheckout(); err != nil {
		return
	}
	if inst, err = g.loadInstance(); err != nil {
		return
	}
	full, ok := repoOnInstance(top, inst)
	if !ok {
		err = fmt.Errorf("this checkout is not hosted on instance %s; run `bottown -i %s repo add` first", inst.Name, inst.Name)
		return
	}
	org, name, _ = strings.Cut(full, "/")
	if repoOverride != "" {
		if o, r, ok := strings.Cut(repoOverride, "/"); ok {
			if o != org {
				err = fmt.Errorf("repository %q is not in org %s", repoOverride, org)
				return
			}
			repoOverride = r
		}
		name = repoOverride
	}
	key, err = ownerKey(g)
	return
}

// discussQuestion is the question for a discuss run: the words after the
// number, or --question-file (- is stdin), not both.
func discussQuestion(cmd *cobra.Command, words []string, file string) (string, error) {
	q := strings.Join(words, " ")
	if file != "" {
		if q != "" {
			return "", errors.New("give the question as arguments or with --question-file, not both")
		}
		b, err := readBody(cmd, "", file)
		if err != nil {
			return "", err
		}
		q = b
	}
	return strings.TrimSpace(q), nil
}

func parseNumber(s string) (int64, error) {
	s = strings.TrimLeft(s, "#!")
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not an issue or pull request number", s)
	}
	return n, nil
}

// errNoChange makes a Workspace.Update leave the record as it is.
var errNoChange = errors.New("record unchanged")

func startRun(cmd *cobra.Command, g *globals, kind, target string, o runOpts) (err error) {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	n, err := parseNumber(target)
	if err != nil {
		return err
	}
	actor, spec, err := runs.SplitActorSpec(o.actor)
	if err != nil {
		return err
	}
	rc, err := openRunContextRepo(g, o.repo)
	if err != nil {
		return err
	}
	if spec == "" {
		spec = rc.inst.ActorModel(actor)
	}
	if err := rc.ws.Sync(ctx); err != nil {
		return err
	}

	req := server.RunRequest{Kind: kind, Actor: actor, Spec: spec, Repo: rc.repo, AllowSameFamily: o.allowSameFamily, BypassActorLimit: o.bypassActorLimit, Interactive: o.interactive}
	if kind == policy.Verify {
		req.PR = n
	} else {
		req.Issue = n
	}
	var created server.RunResponse
	if err := callServer(ctx, rc.inst, rc.key, "POST", "/v1/runs", req, &created); err != nil {
		return err
	}
	run := created.Run
	// From here on, always end the run on the server, whatever happens.
	status := runs.StatusFailed
	var digest secrets.Digest
	phase := launchStarting
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if status != runs.StatusSucceeded {
			if f := launcherFailure(phase, err); f != nil {
				digest.Failure = f
			}
		}
		end := server.EndRunRequest{Status: status, Digest: digest}
		if e := callServer(cctx, rc.inst, rc.key, "DELETE", "/v1/runs/"+run.ID, end, nil); e != nil && err == nil {
			err = fmt.Errorf("end run %s: %w", run.ID, e)
		}
		// A startup that failed before the run was running ends its record too.
		_ = rc.ws.Update(run.ID, func(current *runs.Record) error {
			if current.Status != runs.StatusPreparing {
				return errNoChange
			}
			now := time.Now().UTC()
			current.Status, current.Ended, current.Key = runs.StatusFailed, &now, ""
			return nil
		})
	}()

	harnessName := run.Harness
	if harnessName == "" {
		harnessName = run.Actor
	}
	// The run's attachments last as long as the run: removed however this
	// returns, and by `run gc` if this process dies. The preparing record
	// is saved before anything is downloaded, so gc finds the run, as dead,
	// even if this process dies before the run is running.
	attachDir := rc.ws.AttachmentsDir(run.ID)
	tmpDir := rc.ws.TmpDir(run.ID)
	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family, Instance: rc.inst.Name,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: o.interactive, AllowSameFamily: run.AllowSameFamily, Harness: harnessName, Spec: run.Spec,
		Status: runs.StatusPreparing, PID: os.Getpid(), Started: time.Now().UTC(),
		Key: created.Key, ServerURL: rc.inst.ServerURL(), Question: o.question,
		Message: strings.TrimSpace(o.message), Attachments: attachDir, TmpDir: tmpDir,
		FlowNote: strings.TrimSpace(o.flowNote), ChecksPassed: o.checksPassed,
	}
	if err := rc.ws.Save(rec); err != nil {
		return err
	}
	defer func() {
		if e := runs.RemoveAttachments(attachDir); e != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: remove attachments: %v\n", e)
		}
	}()
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	defer func() {
		// Stop a detached web preview recorded in the scratch directory
		// before it goes, so the preview does not outlive the run.
		stopRunPreviews(cmd.ErrOrStderr(), tmpDir)
		if e := os.RemoveAll(tmpDir); e != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: remove tmp dir: %v\n", e)
		}
	}()
	rk := &agentCtx{Actor: run.Actor, ServerURL: rc.inst.ServerURL(), Key: created.Key, RunID: run.ID}
	prompt, base, err := renderPrompt(ctx, rc, run, rec, rk, attachDir)
	if err != nil {
		return err
	}
	var dir string
	if kind == policy.Implement || kind == policy.Plan {
		dir, err = rc.ws.AddImplement(run.ID, run.Branch, base)
	} else {
		dir, err = rc.ws.AddVerify(run.ID, run.SHA)
	}
	if err != nil {
		return err
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
	if err := rc.ws.Update(run.ID, func(current *runs.Record) error {
		current.Status, current.Worktree = runs.StatusRunning, dir
		rec = *current
		return nil
	}); err != nil {
		return err
	}

	l := runs.Launch{
		Dir: dir, Prompt: prompt, PromptFile: rc.ws.Path(run.ID, ".prompt.md"),
		MCPURL: mcpURL, Key: created.Key,
		RecordFile: recPath, RunID: run.ID, Interactive: o.interactive, Allow: o.allow,
		ReadOnly: kind == policy.Discuss, Spec: run.Spec, TmpDir: tmpDir,
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
	fmt.Fprintf(out, "run %s: %s %s as %s (%s, %s%s) in %s\n", run.ID, kind, rec.Target(), run.Actor, run.Family, rec.Mode(), modelNote(", ", rec.Spec), dir)

	logf, err := os.OpenFile(rc.ws.Path(run.ID, ".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	// The log backs Live Runs and the summary: encoded payloads, such as an
	// image attachment the harness opened, are elided as it is written.
	logw := runs.NewRedactingWriter(logf)
	if o.interactive {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		c.Stdout, c.Stderr = logw, logw
		// Through a pipe, a tool process the harness left behind could
		// hold the log open, and Wait with it.
		c.WaitDelay = 10 * time.Second
		fmt.Fprintf(out, "  log: %s\n", rc.ws.Path(run.ID, ".log"))
		// A batch harness leads its own process group, so run stop and a
		// cancelled launcher reach its MCP servers and tool shells too.
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	}
	started := time.Now()
	if err := c.Start(); err != nil {
		return err
	}
	phase = launchHarnessRan
	if err := rc.ws.Update(run.ID, func(current *runs.Record) error {
		current.HarnessPID = c.Process.Pid
		return nil
	}); err != nil {
		_ = c.Process.Kill()
		_ = c.Wait()
		return err
	}
	t, transcribes := h.(runs.Transcriber)
	transcribes = transcribes && !o.interactive
	livePath := rc.ws.Path(run.ID, runs.LiveTranscriptSuffix)
	var following sync.WaitGroup
	followCtx, stopFollowing := context.WithCancel(context.WithoutCancel(ctx))
	if transcribes {
		// The harness prints only its final message: Live Runs follows the
		// session's transcript so far instead.
		following.Go(func() {
			runs.FollowTranscript(followCtx, t, l, started, livePath, runs.FollowTranscriptEvery)
		})
	}
	runErr := c.Wait()
	stopFollowing()
	following.Wait()
	// Before the log gets the transcript, so that no reader sees it twice.
	_ = os.Remove(livePath)
	if transcribes {
		// Also for a stopped run, whose transcript shows how far it got;
		// bounded, so a stuck export cannot hold the run open.
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		if err := t.Transcript(tctx, l, started, logw); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: run transcript: %v\n", err)
			fmt.Fprintf(logw, "\nbottown: no transcript: %v\n", err)
		}
		cancel()
	}
	if err := logw.Close(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: write log: %v\n", err)
	}
	code := 0
	if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
	}
	if runErr != nil && code == 0 {
		code = -1
	}
	if current, loadErr := rc.ws.Load(run.ID); loadErr == nil {
		rec.StopRequested = current.StopRequested
	}
	status = runs.StatusFailed
	if code == 0 && !rec.StopRequested {
		status = runs.StatusSucceeded
	}
	if o.interactive {
		status = askOutcome(cmd, status)
	}

	logBytes, _ := os.ReadFile(rc.ws.Path(run.ID, ".log"))
	summary := h.Summary(logBytes)

	now := time.Now().UTC()
	if err := rc.ws.Update(run.ID, func(current *runs.Record) error {
		if current.StopRequested {
			status = runs.StatusFailed
		}
		current.Status, current.ExitCode, current.Ended, current.Key = status, &code, &now, ""
		rec = *current
		return nil
	}); err != nil {
		return err
	}
	digest = secrets.Digest{Summary: cleanSummary(rec, summary), Usage: h.Usage(logBytes)}
	if status != runs.StatusSucceeded {
		digest.Failure = observedFailure(ctx.Err(), rec.StopRequested, code, time.Since(started))
	}
	// The digest now says what was observed, if anything: the error this
	// returns for a failed run is its outcome, not a launcher failure.
	phase = launchFinalized
	if err := handover(ctx, rc, run, rec, created.Key, status, code, summary); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: handover comment failed: %v\n", err)
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
		return outcomeError(run.ID, status)
	}
	return nil
}

// outcomeError is what startRun returns for a run that did not succeed.
func outcomeError(id, status string) error {
	return fmt.Errorf("run %s %s", id, status)
}

// launchPhase is how far startRun got, for what its error means.
type launchPhase int

const (
	launchStarting   launchPhase = iota // before the harness ran
	launchHarnessRan                    // the harness ran; the digest is not final
	launchFinalized                     // the digest records the outcome
)

// launcherFailure is the failure startRun's error records for a run that
// did not succeed: before the harness ran, a startup failure; after, the
// launcher could not finish the run. Once the digest is final, the error
// only reports the outcome already recorded, or not observed (a clean exit
// the owner marked failed), and records nothing.
func launcherFailure(phase launchPhase, err error) *secrets.Failure {
	if err == nil || phase == launchFinalized {
		return nil
	}
	cause := secrets.FailureStartup
	if phase == launchHarnessRan {
		cause = secrets.FailureInterrupted
	}
	return &secrets.Failure{Cause: cause, Detail: err.Error()}
}

// observedFailure is what the launcher saw end a run that did not succeed:
// its context's error (a flow's time limit, or a cancelled launcher), an
// owner's stop, or the harness's exit code. It is nil for a clean exit,
// such as an interactive run the owner marked failed.
func observedFailure(ctxErr error, stopped bool, code int, elapsed time.Duration) *secrets.Failure {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return &secrets.Failure{Cause: secrets.FailureTimeout, Detail: fmt.Sprintf("killed at the time limit, after %s", elapsed.Round(time.Second))}
	case ctxErr != nil:
		return &secrets.Failure{Cause: secrets.FailureInterrupted, Detail: "the launcher was cancelled"}
	case stopped:
		return &secrets.Failure{Cause: secrets.FailureStopped, Detail: "stopped by the owner"}
	case code != 0:
		return &secrets.Failure{Cause: secrets.FailureHarnessExit, Detail: fmt.Sprintf("the harness exited with code %d", code), ExitCode: &code}
	}
	return nil
}

// renderPrompt fetches the issue or PR and renders the goal prompt, with
// the owner's question or message from rec, after downloading its
// attachments with the run key rk into attachDir. It also returns the
// repository's default branch.
func renderPrompt(ctx context.Context, rc *runContext, run secrets.Run, rec runs.Record, rk *agentCtx, attachDir string) (string, string, error) {
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
	d := runs.PromptData{
		RunID: run.ID, Actor: run.Actor, Family: run.Family, Repo: run.Org + "/" + run.Repo,
		Issue: run.Issue, PR: run.PR, Title: item.Title, Body: item.Body,
		Branch: run.Branch, Base: base, SHA: run.SHA, Author: run.Author, AllowSameFamily: run.AllowSameFamily,
		Bottown: selfPath(), Remote: rc.inst.Remote(), Question: rec.Question, Message: rec.Message, AttachmentsOf: n,
		FlowNote: rec.FlowNote, ChecksPassed: rec.ChecksPassed,
	}
	// Attachments never stop a run: a failure is in the prompt instead.
	var is server.IssueOut
	if err := rk.call("GET", fmt.Sprintf("/v1/forge/issue?number=%d", n), nil, &is); err != nil {
		d.AttachmentsError = err.Error()
	} else {
		d.Attachments, d.AttachmentsError = rk.fetchAttachments(ctx, attachDir, "", is), is.AttachmentsError
	}
	d.WebPreview = runs.HasWebPreview(rc.ws.Mirror, rc.ws.Remote+"/"+base)
	if run.Kind == policy.Verify {
		d.Reviewed = rk.prReviewed(run.PR)
	}
	if run.Kind == policy.Plan {
		// bottown reads the repository's settings from its default branch.
		settings, err := runs.RepoSettingsAt(rc.ws.Mirror, rc.ws.Remote+"/"+base)
		if err != nil {
			return "", "", err
		}
		d.MilestonesDir = settings.MilestonesDir
	}
	p, err := runs.Prompt(run.Kind, d)
	return p, base, err
}

// modelNote is sep+"model SPEC", or "" for the harness's default model.
func modelNote(sep, spec string) string {
	if spec == "" {
		return ""
	}
	return sep + "model " + spec
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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

// handover posts the run's outcome on its issue (implement, discuss, plan) or PR (verify),
// with the run key, as the actor.
func handover(ctx context.Context, rc *runContext, run secrets.Run, rec runs.Record, key, status string, code int, summary string) error {
	n := run.Issue
	if run.Kind == policy.Verify {
		n = run.PR
	}
	path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", run.Org, run.Repo, n)
	return callServer(ctx, rc.inst, key, "POST", path, map[string]string{"body": handoverText(rec, status, code, summary)}, nil)
}

// cleanSummary is an agent's summary as the forge and the digest show it.
// Agents link files by absolute local path; on the forge, repo-relative
// paths are what make sense.
func cleanSummary(rec runs.Record, summary string) string {
	summary = strings.ReplaceAll(summary, "file://"+rec.Worktree+"/", "")
	summary = strings.ReplaceAll(summary, rec.Worktree+"/", "")
	return strings.TrimSpace(summary)
}

// handoverText is the handover comment for a finished run.
func handoverText(rec runs.Record, status string, code int, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**bottown run `%s`** · %s · %s (%s)", rec.ID, rec.Kind, rec.Actor, rec.Family)
	if rec.Spec != "" {
		fmt.Fprintf(&b, " · model `%s`", rec.Spec)
	}
	fmt.Fprintf(&b, " · %s · **%s**", rec.Mode(), status)
	if code != 0 {
		fmt.Fprintf(&b, " (exit %d)", code)
	}
	b.WriteString("\n\n")
	if rec.StopRequested {
		b.WriteString("Stopped by the owner.\n\n")
	}
	switch rec.Kind {
	case policy.Implement, policy.Plan:
		if head, err := runs.Git(rec.Worktree, nil, "rev-parse", "--short", "HEAD"); err == nil {
			fmt.Fprintf(&b, "Branch `%s` at `%s`.\n\n", rec.Branch, head)
		}
	case policy.Discuss:
		fmt.Fprintf(&b, "Discussed against `%s`, the default branch's head.\n\n", rec.SHA)
		if rec.Question != "" {
			fmt.Fprintf(&b, "Question:%s\n\n", quoteLines(rec.Question))
		}
	default:
		fmt.Fprintf(&b, "Verified at `%s`.\n\n", rec.SHA)
	}
	if rec.Message != "" {
		fmt.Fprintf(&b, "Owner's note:%s\n\n", quoteLines(rec.Message))
	}
	if rec.FlowNote != "" {
		fmt.Fprintf(&b, "Flow's note:%s\n\n", quoteLines(rec.FlowNote))
	}
	if rec.Interactive {
		b.WriteString("_Interactive run: the owner could steer the agent, so this result is partly the owner's work._\n\n")
	}
	if s := cleanSummary(rec, summary); s != "" {
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
		st.Header.Render("MODEL"),
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
			orDash(rec.Spec),
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
			orDash(sr.Spec),
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
		st.Header.Render("MODEL"),
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
			orDash(sr.Spec),
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
			if b, err := runs.ReadLog(logPath); err == nil && len(b) > 0 {
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
			rec.Family+modelNote(", ", rec.Spec),
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
		if sr.Purged {
			fmt.Fprint(out, ", purged (digest kept)")
		}
		fmt.Fprintln(out)
		if sr.Usage != nil {
			fmt.Fprintf(out, "        usage %s\n", formatUsage(*sr.Usage))
		}
		if sr.Summary != "" {
			first, _, _ := strings.Cut(sr.Summary, "\n")
			fmt.Fprintf(out, "        summary %s\n", truncate(first, 200))
		}
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

// formatUsage renders what a harness reported, leaving out what it did not:
// "1174 in, 20 out, 11383 cache read, 5834 cache write tokens · $0.0492 ·
// 1 turns · 2.752s".
func formatUsage(u secrets.Usage) string {
	var tok []string
	for _, f := range []struct {
		n    *int64
		name string
	}{{u.InputTokens, "in"}, {u.OutputTokens, "out"}, {u.CacheReadTokens, "cache read"}, {u.CacheWriteTokens, "cache write"}} {
		if f.n != nil {
			tok = append(tok, fmt.Sprintf("%d %s", *f.n, f.name))
		}
	}
	var parts []string
	if len(tok) > 0 {
		parts = append(parts, strings.Join(tok, ", ")+" tokens")
	}
	if u.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.4f", *u.CostUSD))
	}
	if u.Turns != nil {
		parts = append(parts, fmt.Sprintf("%d turns", *u.Turns))
	}
	if u.DurationMS != nil {
		parts = append(parts, (time.Duration(*u.DurationMS) * time.Millisecond).String())
	}
	if len(parts) == 0 {
		return "none reported"
	}
	return strings.Join(parts, " · ")
}

// quoteLines renders s as a Markdown blockquote, starting on its own line.
func quoteLines(s string) string {
	return "\n> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newRunStop(g *globals) *cobra.Command {
	var grace time.Duration
	cmd := &cobra.Command{
		Use:   "stop ID",
		Short: "Stop a live batch run started from this checkout",
		Long: `Stop a live batch run started from this checkout: send its harness's
process group TERM and, if the harness has not finished within the grace
period, KILL. The launcher then ends the run as failed and posts the stopped
handover.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			id := args[0]
			rec, err := rc.ws.Load(id)
			if err != nil {
				return fmt.Errorf("load local run %s: %w", id, err)
			}
			if rec.Instance != rc.inst.Name || rec.Repo != rc.org+"/"+rc.repo {
				return fmt.Errorf("run %s does not belong to this checkout", id)
			}
			if rec.Interactive {
				return fmt.Errorf("run %s is interactive; stop it in its terminal", id)
			}
			if rec.Status != runs.StatusRunning || rec.Ended != nil {
				return fmt.Errorf("run %s is not live (local status %s)", id, rec.Status)
			}
			// Without its launcher the recorded harness pid may have been
			// reused by an unrelated process: signal nothing.
			if !rec.LauncherAlive() {
				return fmt.Errorf("run %s's launcher (pid %d) is dead; not signalling its harness pid %d", id, rec.PID, rec.HarnessPID)
			}
			var serverRun secrets.Run
			if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs/"+id, nil, &serverRun); err != nil {
				return err
			}
			if serverRun.Interactive || serverRun.Ended != nil || !serverRun.Active(time.Now().UTC()) {
				return fmt.Errorf("run %s is not a live batch run on the server", id)
			}
			if rec.HarnessPID <= 0 {
				return fmt.Errorf("run %s has no harness process yet", id)
			}
			process, err := os.FindProcess(rec.HarnessPID)
			if err != nil || process.Signal(syscall.Signal(0)) != nil {
				return fmt.Errorf("run %s has no live harness process", id)
			}
			if err := rc.ws.Update(id, func(current *runs.Record) error {
				if current.Status != runs.StatusRunning || current.Ended != nil {
					return fmt.Errorf("run %s is no longer live", id)
				}
				current.StopRequested = true
				rec = *current
				return nil
			}); err != nil {
				return err
			}
			if err := signalHarness(rec.HarnessPID, syscall.SIGTERM); err != nil {
				_ = rc.ws.Update(id, func(current *runs.Record) error {
					if current.Ended == nil {
						current.StopRequested = false
					}
					return nil
				})
				return fmt.Errorf("stop run %s: %w", id, err)
			}

			// A harness that ignores TERM gets SIGKILL after the grace
			// period; the launcher then ends the run as failed as usual.
			killAt := time.Now().Add(grace)
			deadline := killAt.Add(30 * time.Second)
			killed := false
			for {
				var ended secrets.Run
				if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs/"+id, nil, &ended); err != nil {
					return err
				}
				current, localErr := rc.ws.Load(id)
				if localErr == nil && current.Ended != nil && ended.Ended != nil {
					if ended.Status != runs.StatusFailed {
						return fmt.Errorf("run %s stopped with unexpected server status %s", id, ended.Status)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "run %s stopped\n", id)
					return nil
				}
				if !killed && time.Now().After(killAt) {
					killed = true
					if !rec.LauncherAlive() {
						return fmt.Errorf("run %s's launcher (pid %d) died after TERM; not killing its harness pid %d", id, rec.PID, rec.HarnessPID)
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "run %s did not finish within %s of TERM; killing its harness\n", id, grace)
					if err := signalHarness(rec.HarnessPID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
						return fmt.Errorf("kill run %s: %w", id, err)
					}
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("run %s was killed but has not finished", id)
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 10*time.Second, "how long the harness has to finish after TERM before it is killed")
	return cmd
}

// signalHarness signals a batch harness's process group, which it leads.
// A harness started before batch runs had their own group has none, so
// it falls back to the process alone.
func signalHarness(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != syscall.ESRCH {
		return err
	}
	return syscall.Kill(pid, sig)
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
whose local files are kept keeps its server record too.

A run whose key expired while it was live cannot end itself, since its key
no longer authenticates: gc ends it as revoked, unless its launcher in this
checkout is still running. Live runs are never purged; --older-than keeps
recently ended ones.`,
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
			removed, keep := gcLocal(rc.ws, recs, failed, cmd.ErrOrStderr(), func(id string) {
				end := server.EndRunRequest{Status: "revoked", Digest: secrets.Digest{Failure: &secrets.Failure{
					Cause: secrets.FailureInterrupted, Detail: "its launcher went away before ending it; revoked by run gc"}}}
				_ = callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+id, end, nil)
			})
			// A run whose key expired while it was live cannot end itself:
			// its key no longer authenticates. Revoke it with the owner key.
			var live []secrets.Run
			if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs?state=live&repo="+rc.repo, nil, &live); err != nil {
				return fmt.Errorf("removed %d runs; listing live server runs: %w", removed, err)
			}
			revoked := 0
			for _, id := range expiredLive(live, recs, time.Now().UTC()) {
				end := server.EndRunRequest{Status: "revoked", Digest: secrets.Digest{Failure: &secrets.Failure{
					Cause: secrets.FailureExpired, Detail: "its key expired while it was live; revoked by run gc"}}}
				if err := callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+id, end, nil); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: revoke %s: %v\n", id, err)
					continue
				}
				revoked++
			}
			var purged struct {
				Purged []string `json:"purged"`
			}
			req := server.PurgeRequest{Repo: rc.repo, EndedBefore: time.Now().Add(-olderThan), Keep: keep}
			if err := callServer(cmd.Context(), rc.inst, rc.key, "POST", "/v1/runs/purge", req, &purged); err != nil {
				return fmt.Errorf("removed %d runs; purging server records: %w", removed, err)
			}
			if revoked > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "revoked %d runs whose key expired while live\n", revoked)
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

// gcLocal removes the worktrees and files of finished local runs: succeeded
// and dead, and failed ones too with failed. A dead run, including one whose
// launcher died while preparing, is revoked first. keep is the runs it left,
// whose server records stay.
func gcLocal(ws *runs.Workspace, recs []runs.Record, failed bool, warn io.Writer, revoke func(id string)) (removed int, keep []string) {
	for _, r := range recs {
		switch r.Status {
		case runs.StatusPreparing, runs.StatusRunning:
			keep = append(keep, r.ID)
			continue
		case runs.StatusFailed:
			if !failed {
				keep = append(keep, r.ID)
				continue
			}
		case runs.StatusDead:
			// Its launcher died: make sure the key is gone too.
			revoke(r.ID)
		}
		if err := ws.Remove(r.ID); err != nil {
			fmt.Fprintf(warn, "warning: %s: %v\n", r.ID, err)
			keep = append(keep, r.ID)
			continue
		}
		// A detached preview recorded in the run's scratch directory ends
		// with the run, before RemoveFiles removes that directory.
		stopRunPreviews(warn, r.TmpDir)
		ws.RemoveFiles(r.ID)
		removed++
	}
	_, _ = runs.Git(ws.Mirror, nil, "worktree", "prune")
	return removed, keep
}

// expiredLive is the IDs of the server runs gc revokes: not ended, key
// expired. A run whose launcher in this checkout is still alive is left to
// it: the launcher ends the run with the owner key when the harness exits.
func expiredLive(live []secrets.Run, local []runs.Record, now time.Time) []string {
	launched := make(map[string]bool)
	for _, r := range local {
		if (r.Status == runs.StatusPreparing || r.Status == runs.StatusRunning) && r.Ended == nil && r.LauncherAlive() {
			launched[r.ID] = true
		}
	}
	var ids []string
	for _, r := range live {
		if r.Ended == nil && !r.Expires.IsZero() && !now.Before(r.Expires) && !launched[r.ID] {
			ids = append(ids, r.ID)
		}
	}
	return ids
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
