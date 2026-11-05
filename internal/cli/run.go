package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

type runOpts struct {
	actor           string
	repo            string
	interactive     bool
	allowSameFamily bool
	serializeActor  bool // implement, verify: refuse the run while the actor has another live batch run
	allow           []string
	keep            bool
	question        string    // discuss: the owner's question for the prompt
	message         string    // implement, verify, plan: the owner's note for the prompt
	flowNote        string    // implement, verify: bottown flow's note (the post-implement checks)
	checksPassed    bool      // verify: the flow's post-implement checks passed at sha
	sha             string    // verify: the head the run must be pinned at (bottown flow)
	synced          bool      // the mirror was fetched for an earlier try of this run (bottown flow, waiting for a busy actor)
	tail            *liveTail // the live tail the run shows in, if bottown flow runs one
	plain           bool      // no live tail: plain output even on a terminal (bottown flow without a tail, --unattended included)
}

// ownTail is whether startRun shows a live tail of its own for the run,
// when its output is a terminal: a batch run that neither shows in a
// flow's tail nor asks for plain output.
func (o runOpts) ownTail() bool { return o.tail == nil && !o.interactive && !o.plain }

// errHeadMoved is a verify run refused because the PR's head is not runOpts.sha.
var errHeadMoved = errors.New(server.HeadMovedMessage)

// errActorBusy is a run with runOpts.serializeActor refused because its
// actor has another live batch run.
var errActorBusy = errors.New(server.ActorBusyMessage)

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
		c.Flags().BoolVar(&o.serializeActor, "serialize-actors", false, "refuse the run while the actor has another live batch run (by default an actor's batch runs on different targets run concurrently)")
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

func startRun(cmd *cobra.Command, g *globals, kind, target string, o runOpts) (err error) {
	ctx := cmd.Context()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	// A batch run on a terminal shows its live tail there (tail.go), a
	// flow's for all its runs; its lines and the launcher's go through it.
	// Closed last, after every deferred line below.
	tail := o.tail
	if o.ownTail() {
		if f, ok := terminalOut(cmd); ok {
			tail = newLiveTail(f)
			defer tail.Close()
		}
	}
	if tail != nil {
		out, errOut = tail, tail
	} else {
		out = colorOut(out)
	}
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
	if !o.synced {
		if err := rc.ws.Sync(ctx); err != nil {
			return err
		}
	}

	if kind == policy.Implement || kind == policy.Plan {
		if err := rc.ws.PrepareImplement(fmt.Sprintf("issue/%d", n)); err != nil {
			return err
		}
	}

	req := server.RunRequest{Kind: kind, Actor: actor, Spec: spec, Repo: rc.repo, AllowSameFamily: o.allowSameFamily, SerializeActor: o.serializeActor, Interactive: o.interactive}
	if kind == policy.Verify {
		req.PR, req.SHA = n, o.sha
	} else {
		req.Issue = n
	}
	var created server.RunResponse
	if err := callServer(ctx, rc.inst, rc.key, "POST", "/v1/runs", req, &created); err != nil {
		if errors.Is(err, client.ErrHeadMoved) {
			return fmt.Errorf("%w: %v", errHeadMoved, err)
		}
		if errors.Is(err, client.ErrActorBusy) {
			return fmt.Errorf("%w: %w", errActorBusy, err)
		}
		return err
	}
	run := created.Run
	lc := launchedLifecycle(rc, run)
	// The launcher's lock (see below) is released last, after the deferred
	// end below has finished the record.
	releaseLauncher := func() {}
	defer func() { releaseLauncher() }()
	// From here on, always end the run on the server, whatever happens; a
	// failed setup has already (see Lifecycle.Prepare).
	status := runs.StatusFailed
	var digest secrets.Digest
	phase := launchStarting
	defer func() {
		if status != runs.StatusSucceeded {
			if f := launcherFailure(phase, err); f != nil {
				digest.Failure = f
			}
		}
		e := lc.End(ctx, status, digest)
		switch {
		case e != nil && err == nil:
			err = e
		case e != nil:
			fmt.Fprintf(errOut, "warning: %v\n", e)
		}
	}()

	// A server that predates RunRequest.SHA pins whatever the head is.
	if req.SHA != "" && run.SHA != req.SHA {
		return fmt.Errorf("%w: pull request #%d is at %s, not %s", errHeadMoved, run.PR, run.SHA, req.SHA)
	}

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
	// Held while this process lives, so gc and run stop know exactly when
	// it has died, even if its pid is reused.
	liveLock, release, err := rc.ws.HoldLauncher(run.ID)
	if err != nil {
		return err
	}
	releaseLauncher = release
	rec := runs.Record{
		ID: run.ID, Kind: run.Kind, Actor: run.Actor, Family: run.Family, Instance: rc.inst.Name,
		Repo: run.Org + "/" + run.Repo, Issue: run.Issue, PR: run.PR, Branch: run.Branch, SHA: run.SHA,
		Interactive: o.interactive, AllowSameFamily: run.AllowSameFamily, Harness: harnessName, Spec: run.Spec,
		PID: os.Getpid(), LauncherLock: liveLock, Started: time.Now().UTC(),
		Key: created.Key, ServerURL: rc.inst.ServerURL(), Question: o.question,
		Message: strings.TrimSpace(o.message), Attachments: attachDir, TmpDir: tmpDir,
		FlowNote: strings.TrimSpace(o.flowNote), ChecksPassed: o.checksPassed,
	}
	defer func() {
		if e := runs.RemoveAttachments(attachDir); e != nil {
			fmt.Fprintf(errOut, "warning: remove attachments: %v\n", e)
		}
	}()
	defer func() {
		// Stop a detached web preview recorded in the scratch directory
		// before it goes, so the preview does not outlive the run.
		stopRunPreviews(context.WithoutCancel(ctx), errOut, tmpDir)
		if e := os.RemoveAll(tmpDir); e != nil {
			fmt.Fprintf(errOut, "warning: remove tmp dir: %v\n", e)
		}
	}()
	recPath := rc.ws.Path(run.ID, ".json")
	mcpURL := rc.inst.ServerURL() + "/mcp"
	var prompt, dir string
	var h runs.Harness
	if err := lc.Prepare(ctx, rec, func() error {
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return err
		}
		rk := &agentCtx{Actor: run.Actor, ServerURL: rc.inst.ServerURL(), Key: created.Key, RunID: run.ID}
		p, base, err := renderPrompt(ctx, rc, run, rec, rk, attachDir)
		if err != nil {
			return err
		}
		prompt = p
		if kind == policy.Implement || kind == policy.Plan {
			dir, err = rc.ws.AddImplement(run.ID, run.Branch, base)
		} else {
			dir, err = rc.ws.AddVerify(run.ID, run.SHA)
		}
		if err != nil {
			return err
		}
		if h, err = runs.LookupHarness(harnessName); err != nil {
			return err
		}
		if err := runs.Configure(dir, run.Actor, fmt.Sprintf("!%q credential --run-file %q", selfPath(), recPath)); err != nil {
			return err
		}
		if err := os.WriteFile(rc.ws.Path(run.ID, ".prompt.md"), []byte(prompt), 0o600); err != nil {
			return err
		}
		return lc.Running(func(r *runs.Record) { r.Worktree = dir })
	}); err != nil {
		return err
	}
	rec = lc.Record()

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

	logPath := rc.ws.Path(run.ID, ".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
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
		fmt.Fprintf(out, "  log: %s\n", logPath)
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
	if err := lc.Update(func(current *runs.Record) error {
		current.HarnessPID = c.Process.Pid
		return nil
	}); err != nil {
		_ = c.Process.Kill()
		_ = c.Wait()
		return err
	}
	tailEnded := func() {}
	if tail != nil {
		// The run's limit: the flow's timeout, else its key's lifetime.
		limit := run.Expires.Sub(started)
		if deadline, ok := ctx.Deadline(); ok {
			limit = deadline.Sub(started)
		}
		tailEnded = tail.Follow(tailRun{
			ID: run.ID, Kind: kind, Target: rec.Target(), Actor: run.Actor, Family: run.Family,
			Started: started, Limit: limit,
		}, harnessName, logPath)
		defer tailEnded()
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
			fmt.Fprintf(errOut, "warning: run transcript: %v\n", err)
			fmt.Fprintf(logw, "\nbottown: no transcript: %v\n", err)
		}
		cancel()
	}
	if err := logw.Close(); err != nil {
		fmt.Fprintf(errOut, "warning: write log: %v\n", err)
	}
	// The log is complete: its last lines go to the scrollback.
	tailEnded()
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

	logBytes, _ := os.ReadFile(logPath)
	summary := h.Summary(logBytes)

	if rec, err = lc.Finished(func(current *runs.Record) {
		if current.StopRequested {
			status = runs.StatusFailed
		}
		current.Status, current.ExitCode = status, &code
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
		fmt.Fprintf(errOut, "warning: handover comment failed: %v\n", err)
	}

	st := newStyles()
	if status == runs.StatusSucceeded && !o.keep {
		if err := rc.ws.Remove(run.ID); err != nil {
			fmt.Fprintf(errOut, "warning: remove worktree: %v\n", err)
		}
		fmt.Fprintf(out, "run %s %s (worktree removed)\n", st.Info.Bold(true).Render(run.ID), st.runStatus(status))
	} else {
		fmt.Fprintf(out, "run %s %s (exit %d); worktree kept at %s\n", st.Info.Bold(true).Render(run.ID), st.runStatus(status), code, dir)
	}
	if status == runs.StatusSucceeded {
		return nil
	}
	failed := &runs.FailedRunError{ID: run.ID, Status: status}
	if !o.interactive {
		// Why it failed is often only in the log: a provider error, an
		// actor out of quota or not signed in.
		failed.Log = logPath
		failed.LastError, failed.ToolCalls = runs.LastError(harnessName, logBytes)
		fmt.Fprintf(out, "  log: %s\n", logPath)
		if failed.LastError != "" {
			fmt.Fprintf(out, "  last error: %s\n", st.Danger.Render(failed.LastError))
		}
	}
	return failed
}

// launchedLifecycle is the lifecycle of a run this process launches, just
// created on the server: the owner key ends it there.
func launchedLifecycle(rc *runContext, run secrets.Run) *runs.Lifecycle {
	lc := &runs.Lifecycle{
		Mode:  runs.Launched,
		Store: rc.ws.Store(run.ID),
		EndRun: func(ctx context.Context, id, status string, d secrets.Digest) (secrets.Run, error) {
			return secrets.Run{}, callServer(ctx, rc.inst, rc.key, "DELETE", "/v1/runs/"+id, server.EndRunRequest{Status: status, Digest: d}, nil)
		},
	}
	// The run exists on the server from here, whether or not its record
	// is ever saved.
	lc.Resume(runs.Record{ID: run.ID, Actor: run.Actor})
	return lc
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

// renderPrompt fetches the issue or PR and renders a launched run's goal
// prompt, with the owner's question or message from rec, after downloading
// its attachments with the run key rk into attachDir. It also returns the
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
		Title: item.Title, Body: item.Body, Base: base,
		Bottown: selfPath(), Remote: rc.inst.Remote(), Question: rec.Question, Message: rec.Message,
		FlowNote: rec.FlowNote, ChecksPassed: rec.ChecksPassed,
	}
	// Attachments never stop a launched run: a failure is in the prompt
	// instead.
	var is server.IssueOut
	isErr := rk.call(ctx, "GET", fmt.Sprintf("/v1/forge/issue?number=%d", n), nil, &is)
	p, err := goalPrompt(ctx, rk, run, d, rc.ws.Mirror, rc.ws.Remote+"/"+base, attachDir, &is, isErr)
	return p, base, err
}

// goalPrompt renders run's goal prompt, for either kind of run: d holds
// what the caller knows (the issue's text, the default branch, the
// owner's notes), and goalPrompt adds the run and what the run key rk
// fetches: item's attachments, into attachDir (or itemErr, why item could
// not be read), and a verifier's earlier reviews. gitDir has the
// repository's refs; baseRef names its default branch there, whose
// settings a plan run follows.
func goalPrompt(ctx context.Context, rk *agentCtx, run secrets.Run, d runs.PromptData, gitDir, baseRef, attachDir string, item *server.IssueOut, itemErr error) (string, error) {
	d.RunID, d.Actor, d.Family, d.Repo = run.ID, run.Actor, run.Family, run.Org+"/"+run.Repo
	d.Issue, d.PR, d.Branch, d.SHA, d.Author, d.AllowSameFamily = run.Issue, run.PR, run.Branch, run.SHA, run.Author, run.AllowSameFamily
	d.AttachmentsOf = run.Issue
	if run.Kind == policy.Verify {
		d.AttachmentsOf = run.PR
	}
	if itemErr != nil {
		d.AttachmentsError = itemErr.Error()
	} else {
		d.Attachments, d.AttachmentsError = rk.fetchAttachments(ctx, attachDir, "", *item), item.AttachmentsError
	}
	d.WebPreview = runs.HasWebPreview(gitDir, baseRef)
	if run.Kind == policy.Verify {
		d.Reviewed = rk.prReviewed(ctx, run.PR)
	}
	if run.Kind == policy.Plan {
		// bottown reads the repository's settings from its default branch.
		settings, err := runs.RepoSettingsAt(gitDir, baseRef)
		if err != nil {
			return "", err
		}
		d.MilestonesDir = settings.MilestonesDir
	}
	return runs.Prompt(run.Kind, d)
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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
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
		b.WriteString(secrets.TruncateUTF8(s, secrets.MaxSummary) + "\n")
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
			local, err := rc.ws.Records(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			var remote []secrets.Run
			if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/runs?repo="+rc.repo, nil, &remote); err != nil {
				return err
			}
			out := colorOut(cmd.OutOrStdout())
			return printRunLs(out, local, remote)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list the server's runs for every repository on the instance")
	return cmd
}

func printRunLs(out io.Writer, local []runs.Record, remote []secrets.Run) error {
	byID := map[string]secrets.Run{}
	for _, rem := range remote {
		byID[rem.ID] = rem
	}
	st := newStyles()
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
	var unreadable []runs.Record
	for _, rec := range local {
		seen[rec.ID] = true
		keySt := "gone"
		sr, onServer := byID[rec.ID]
		if onServer {
			keySt = serverKeyState(sr)
		}
		if rec.Status == runs.StatusUnknown {
			unreadable = append(unreadable, rec)
			// The record says nothing: show what the server knows.
			kind, target, actor, family, spec, branch, started := "-", "-", "-", "-", "-", "-", "-"
			if onServer {
				kind, actor, family, spec, branch = sr.Kind, sr.Actor, sr.Family, orDash(sr.Spec), branchOrSHA(sr.Branch, sr.SHA)
				target = fmt.Sprintf("#%d", sr.Issue)
				if sr.PR > 0 {
					target = fmt.Sprintf("!%d", sr.PR)
				}
				started = age(sr.Created)
			}
			tw.Row(
				st.Info.Bold(true).Render(rec.ID),
				st.Purple.Render(kind),
				st.Bold.Render(target),
				st.Info.Render(actor),
				family,
				spec,
				"-",
				st.runStatus(rec.Status),
				st.keyState(keySt),
				branch,
				st.Dim.Render(started),
				"-",
			)
			continue
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
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, rec := range unreadable {
		if _, err := fmt.Fprintf(out, "%s: unreadable run record (status unknown): %s\n", rec.ID+".json", rec.LoadError); err != nil {
			return err
		}
	}
	return nil
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
	out := colorOut(cmd.OutOrStdout())
	return printRunLsAll(out, remote)
}

func printRunLsAll(out io.Writer, remote []secrets.Run) error {
	st := newStyles()
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
			out := colorOut(cmd.OutOrStdout())
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
			printRunShow(out, rec, sr, srErr, logPath, logContent)
			return nil
		},
	}
}

func printRunShow(out io.Writer, rec *runs.Record, sr *secrets.Run, srErr error, logPath, logContent string) {
	st := newStyles()
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
			fmt.Fprintf(out, "        summary %s\n", secrets.TruncateUTF8(first, 200))
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
			fmt.Fprintf(out, "  %s\n", secrets.TruncateUTF8(l, 200))
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

// runStopPoll is how often run stop asks whether the run has ended.
var runStopPoll = time.Second

func newRunStop(g *globals) *cobra.Command {
	var grace time.Duration
	var revoke bool
	cmd := &cobra.Command{
		Use:   "stop ID",
		Short: "Stop a live batch run started from this checkout",
		Long: `Stop a live batch run started from this checkout: send its harness's
process group TERM and, if the harness has not finished within the grace
period, KILL. The launcher then ends the run as failed and posts the stopped
handover.

With --revoke, end a live server run of this repository that no launcher in
this checkout holds, as revoked, and delete its key: one whose launcher
died, or whose local record is gone. It signals no process. A run whose
launcher is still running is refused: stop it without --revoke.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			id := args[0]
			if revoke {
				return revokeRun(cmd, rc, id)
			}
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
				case <-time.After(runStopPoll):
				}
			}
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 10*time.Second, "how long the harness has to finish after TERM before it is killed")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "end a live run that no launcher in this checkout holds as revoked, signalling nothing")
	return cmd
}

// revokeRun is `run stop --revoke ID`: it ends a live server run of this
// checkout's repository that no local launcher holds, as revoked.
func revokeRun(cmd *cobra.Command, rc *runContext, id string) error {
	var serverRun secrets.Run
	if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs/"+id, nil, &serverRun); err != nil {
		return err
	}
	var local *runs.Record
	if rec, err := rc.ws.Load(id); err == nil {
		local = &rec
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load local run %s: %w", id, err)
	}
	if err := revokeRefusal(serverRun, local, rc.org, rc.repo); err != nil {
		return err
	}
	end := server.EndRunRequest{Status: "revoked", Digest: secrets.Digest{Failure: &secrets.Failure{
		Cause: secrets.FailureInterrupted, Detail: "no launcher held it; revoked by the owner with run stop --revoke"}}}
	if err := callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+id, end, nil); err != nil {
		return fmt.Errorf("revoke run %s: %w", id, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run %s revoked\n", id)
	return nil
}

// revokeRefusal is why `run stop --revoke` leaves a server run alone, or
// nil: it must belong to this checkout's repository and be live, and no
// running launcher in this checkout may hold it (local is its record here,
// if any), since that launcher ends it itself.
func revokeRefusal(run secrets.Run, local *runs.Record, org, repo string) error {
	if run.Org != org || run.Repo != repo {
		return fmt.Errorf("run %s is not a run of %s/%s", run.ID, org, repo)
	}
	if run.Ended != nil {
		return fmt.Errorf("run %s already ended (%s)", run.ID, run.Status)
	}
	if local != nil && local.Ended == nil && (local.Status == runs.StatusPreparing || local.Status == runs.StatusRunning) && local.LauncherAlive() {
		return fmt.Errorf("run %s's launcher (pid %d) is still running; stop it with `bottown run stop %s`", run.ID, local.PID, run.ID)
	}
	return nil
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
	var failed, orphans bool
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
checkout is still running. It also revokes, whatever its expiry, a run
that ended locally but is still live on the server, whose launcher could
not end it there (the server was restarting, say). A dead run that cannot be revoked (the server is
unreachable, say) keeps its local record, with a warning, so a later gc
retries. With --orphans gc also revokes this repository's live runs that
the owner started but that have no local record in this checkout, such as
one whose record a gc removed while the server was down; runs that actors
started in their own worktrees are left alone. Live runs are never purged;
--older-than keeps recently ended ones. A run whose local record cannot be read is kept, with a
warning; run ls lists it as unknown.

gc also removes the logs flows leave: bottown flow's e2e logs
(.bottown/runs/flow-<issue>-<sha>.e2e.log) once the issue's PR is merged or
closed, or with --older-than once they are that old, and the web UI's flow
logs (.bottown/flow-<issue>.log) once that flow is not running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := openRunContext(g)
			if err != nil {
				return err
			}
			return gcRuns(cmd, rc, failed, orphans, olderThan)
		},
	}
	cmd.Flags().BoolVar(&failed, "failed", false, "also remove failed runs (kept by default for inspection)")
	cmd.Flags().BoolVar(&orphans, "orphans", false, "also revoke live runs the owner started that have no local record in this checkout")
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "purge only server records of runs that ended at least this long ago (e.g. 168h); also remove e2e logs this old")
	return cmd
}

// gcRuns is `run gc` in the checkout rc.
func gcRuns(cmd *cobra.Command, rc *runContext, failed, orphans bool, olderThan time.Duration) error {
	recs, err := rc.ws.Records(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	// The server's live runs first: a run that ended locally may
	// still be live there, if its launcher could not end it (#243),
	// and its record is gc's only trace of it.
	var live []secrets.Run
	if err := callServer(cmd.Context(), rc.inst, rc.key, "GET", "/v1/runs?state=live&repo="+rc.repo, nil, &live); err != nil {
		return fmt.Errorf("listing live server runs: %w", err)
	}
	removed, keep := gcLocal(rc.ws, recs, failed, serverLive(live), cmd.ErrOrStderr(), func(r runs.Record) error {
		detail := "its launcher went away before ending it; revoked by run gc"
		if r.Status != runs.StatusDead {
			detail = "it ended locally (" + r.Status + ") but its launcher could not end it on the server; revoked by run gc"
		}
		end := server.EndRunRequest{Status: "revoked", Digest: secrets.Digest{Failure: &secrets.Failure{
			Cause: secrets.FailureInterrupted, Detail: detail}}}
		return callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+r.ID, end, nil)
	})
	// A run whose key expired while it was live cannot end itself:
	// its key no longer authenticates. Revoke it with the owner key.
	now := time.Now().UTC()
	revokeAll := func(ids []string, f secrets.Failure) (n int) {
		for _, id := range ids {
			end := server.EndRunRequest{Status: "revoked", Digest: secrets.Digest{Failure: &f}}
			if err := callServer(cmd.Context(), rc.inst, rc.key, "DELETE", "/v1/runs/"+id, end, nil); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: revoke %s: %v\n", id, err)
				continue
			}
			n++
		}
		return n
	}
	expired := expiredLive(live, recs, now)
	revoked := revokeAll(expired, secrets.Failure{
		Cause: secrets.FailureExpired, Detail: "its key expired while it was live; revoked by run gc"})
	orphaned := 0
	if orphans {
		var ids []string
		for _, id := range orphanLive(live, recs, now) {
			if !slices.Contains(expired, id) {
				ids = append(ids, id)
			}
		}
		orphaned = revokeAll(ids, secrets.Failure{
			Cause: secrets.FailureInterrupted, Detail: "no launcher in the owner's checkout held it; revoked by run gc --orphans"})
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
	if orphaned > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "revoked %d orphaned live runs with no local record\n", orphaned)
	}
	open, err := openPRIssues(cmd.Context(), rc)
	if err != nil {
		return fmt.Errorf("removed %d runs, purged %d ended runs from the server; listing open PRs: %w", removed, len(purged.Purged), err)
	}
	logs := gcFlowLogs(rc.ws, open, olderThan, time.Now(), cmd.ErrOrStderr())
	fmt.Fprintf(cmd.OutOrStdout(), "removed %d runs, purged %d ended runs from the server, removed %d flow logs; .bottown is %s\n",
		removed, len(purged.Purged), logs, humanBytes(runs.DirSize(rc.ws.Dir)))
	return nil
}

// openPRIssues is the set of issues of rc's repository with an open PR from
// their issue/<n> branch.
func openPRIssues(ctx context.Context, rc *runContext) (map[int64]bool, error) {
	open := make(map[int64]bool)
	for page := 1; ; page++ {
		var prs []struct {
			Head struct{ Ref string }
		}
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=open&limit=50&page=%d", rc.org, rc.repo, page)
		if err := callServer(ctx, rc.inst, rc.key, "GET", path, nil, &prs); err != nil {
			return nil, err
		}
		for _, pr := range prs {
			if n, ok := issueBranchNumber(pr.Head.Ref); ok {
				open[n] = true
			}
		}
		if len(prs) < 50 {
			return open, nil
		}
	}
}

// issueBranchNumber is n for a branch issue/<n>.
func issueBranchNumber(ref string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(ref, "issue/"), 10, 64)
	return n, err == nil && n > 0 && strings.HasPrefix(ref, "issue/")
}

// gcFlowLogs removes the logs flows leave in ws, and returns how many:
//
//   - <runs>/flow-<issue>-<sha>.e2e.log, bottown flow's e2e log for one PR
//     head, once the issue has no open PR (in open), or once it is at least
//     olderThan old when olderThan is set. While the PR is open the log is
//     kept: a resumed flow points to it rather than checking again.
//   - .bottown/flow-<issue>.log, a web UI flow's output, once the flow is
//     not running: no flow-<issue>.json whose process is alive. A stale
//     flow-<issue>.json goes with it.
func gcFlowLogs(ws *runs.Workspace, open map[int64]bool, olderThan time.Duration, now time.Time, warn io.Writer) (removed int) {
	remove := func(path string) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(warn, "warning: %v\n", err)
			return
		}
		removed++
	}
	e2e, _ := filepath.Glob(filepath.Join(ws.RunsDir, "flow-*.e2e.log"))
	for _, path := range e2e {
		issue, sha, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "flow-"), ".e2e.log"), "-")
		n, err := strconv.ParseInt(issue, 10, 64)
		if !ok || sha == "" || err != nil || n <= 0 {
			continue
		}
		old := false
		if olderThan > 0 {
			if fi, err := os.Stat(path); err == nil && now.Sub(fi.ModTime()) >= olderThan {
				old = true
			}
		}
		if !open[n] || old {
			remove(path)
		}
	}
	flows, _ := filepath.Glob(filepath.Join(ws.Dir, "flow-*.log"))
	for _, path := range flows {
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "flow-"), ".log"), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		info := filepath.Join(ws.Dir, fmt.Sprintf("flow-%d.json", n))
		if b, err := os.ReadFile(info); err == nil {
			var rec struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(b, &rec) == nil && runs.ProcessAlive(rec.PID) {
				continue
			}
			_ = os.Remove(info)
		}
		remove(path)
	}
	return removed
}

// serverLive is the set of the IDs of the runs in live that have not ended.
func serverLive(live []secrets.Run) map[string]bool {
	ids := make(map[string]bool, len(live))
	for _, r := range live {
		if r.Ended == nil {
			ids[r.ID] = true
		}
	}
	return ids
}

// gcLocal removes the worktrees and files of finished local runs: succeeded
// and dead, and failed ones too with failed. A dead run, including one whose
// launcher died while preparing, is revoked first, and so is a succeeded or
// failed one that is still in live, the server's live runs, failed or not:
// its launcher could not end it there (#243). A run whose revoke fails is
// kept, with a warning, so a later gc retries. A run whose record is
// unreadable is kept, with a warning. The lock files of runs whose record
// is gone are removed too. keep is the runs it left, whose server records
// stay.
func gcLocal(ws *runs.Workspace, recs []runs.Record, failed bool, live map[string]bool, warn io.Writer, revoke func(r runs.Record) error) (removed int, keep []string) {
	for _, r := range recs {
		switch r.Status {
		case runs.StatusPreparing, runs.StatusRunning:
			keep = append(keep, r.ID)
			continue
		case runs.StatusUnknown:
			// Its record could not be read, so whether it is finished is
			// unknown: leave it, and its server record, for the owner.
			fmt.Fprintf(warn, "warning: %s.json: unreadable run record (%s); kept\n", r.ID, r.LoadError)
			keep = append(keep, r.ID)
			continue
		}
		// Its launcher died, or ended it here but not on the server: make
		// sure the key is gone too. If that fails, its record is its only
		// local trace: keep it, so a later gc retries.
		if r.Status == runs.StatusDead || live[r.ID] {
			if err := revoke(r); err != nil {
				fmt.Fprintf(warn, "warning: revoke %s run %s: %v; kept, a later gc retries\n", r.Status, r.ID, err)
				keep = append(keep, r.ID)
				continue
			}
		}
		if r.Status == runs.StatusFailed && !failed {
			keep = append(keep, r.ID)
			continue
		}
		if err := ws.Remove(r.ID); err != nil {
			fmt.Fprintf(warn, "warning: %s: %v\n", r.ID, err)
			keep = append(keep, r.ID)
			continue
		}
		// A detached preview recorded in the run's scratch directory ends
		// with the run, before RemoveFiles removes that directory.
		stopRunPreviews(context.Background(), warn, r.TmpDir)
		ws.RemoveFiles(r.ID)
		removed++
	}
	// Lock files left by runs removed before RemoveFiles removed them.
	ws.RemoveOrphanLocks()
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

// orphanGrace is how old a live server run must be before gc --orphans
// takes its missing local record as lost: a launcher creates the server run
// moments before it saves its record.
const orphanGrace = time.Minute

// orphanLive is the IDs of the server runs gc --orphans revokes: not ended,
// started by the owner rather than self-started by its actor in an actor
// worktree (whose record lives there), created at least orphanGrace ago,
// and with no local record in this checkout.
func orphanLive(live []secrets.Run, local []runs.Record, now time.Time) []string {
	recorded := make(map[string]bool)
	for _, r := range local {
		recorded[r.ID] = true
	}
	var ids []string
	for _, r := range live {
		if r.Ended == nil && r.StartedBy != "" && r.StartedBy != r.Actor && now.Sub(r.Created) >= orphanGrace && !recorded[r.ID] {
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
