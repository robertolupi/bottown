package cli

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/webui"
	"github.com/spf13/cobra"
)

//go:embed webshot_metrics.js
var shotMetricsJS string

// browserEnv names the browser `web shot` drives, ahead of the ones it
// looks for.
const browserEnv = "BOTTOWN_BROWSER"

// shotSettings holds web shot's flags.
type shotSettings struct {
	fixture   string
	path      string
	widths    []int
	height    int
	schemes   []string
	selectors []string
	eval      string
	wait      string
	out       string
	browser   string
	timeout   time.Duration
}

func newWebShot() *cobra.Command {
	var s shotSettings
	cmd := &cobra.Command{
		Use:   "shot",
		Short: "Capture this checkout's web UI: layout metrics, rendered DOM and screenshots, then exit",
		Long: `Build the web UI in this checkout's web/, serve it with a fixture as
web preview does, load it in a headless Chrome or Chromium, and write
what the page rendered, for each width and color scheme:

  metrics.json            per case: the panels in document order with
                          their boxes, every element that scrolls or has
                          a max-height (scrollHeight vs clientHeight, the
                          computed max-height), what overflows the
                          viewport sideways, --selector matches, the
                          --eval result, and the page's console errors
  WIDTH-SCHEME.png        a full-page screenshot
  WIDTH-SCHEME.dom.html   the DOM after the scripts ran
  build/                  the built index.html and assets (CSS, JS)

It prints the paths, metrics.json first, and exits: it never blocks the
shell. The metrics are for runs and tests to assert on; the screenshots are
for people.

It drives the browser in --browser, else $BOTTOWN_BROWSER, else an
installed Chrome, Chromium or Edge. Without one, or where the browser may
not start (a sandboxed run), it says so and fails; web preview --detach
still serves the page for someone to open.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"agent": "true"}, // needs no instance
		RunE: func(cmd *cobra.Command, _ []string) error {
			return webShot(cmd, s)
		},
	}
	f := cmd.Flags()
	f.StringVar(&s.fixture, "fixture", "default", "the scenario in web/fixtures to serve")
	f.StringVar(&s.path, "path", "/", "the page to load, such as /#milestones")
	f.IntSliceVar(&s.widths, "width", []int{1440, 390}, "viewport widths to capture")
	f.IntVar(&s.height, "height", 900, "viewport height")
	f.StringSliceVar(&s.schemes, "scheme", []string{"light", "dark"}, "color schemes to capture (light, dark)")
	f.StringArrayVar(&s.selectors, "selector", nil, "also measure the elements this CSS selector matches (repeatable)")
	f.StringVar(&s.eval, "eval", "", "a JavaScript expression to evaluate in each case; its JSON value goes in the metrics")
	f.StringVar(&s.wait, "wait", "", "a CSS selector to wait for before capturing")
	f.StringVar(&s.out, "out", "", "the directory to write to (default: a new temporary directory)")
	f.StringVar(&s.browser, "browser", "", "the Chrome or Chromium executable (default: $"+browserEnv+", else an installed one)")
	f.DurationVar(&s.timeout, "timeout", 2*time.Minute, "how long the browser may take for all the cases")
	return cmd
}

// shotCase is one page capture: a viewport width and a color scheme.
type shotCase struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Scheme string `json:"scheme"`
}

// shotCases is the matrix of widths and schemes, in the order given.
func shotCases(widths []int, height int, schemes []string) ([]shotCase, error) {
	if len(widths) == 0 || len(schemes) == 0 {
		return nil, errors.New("need at least one --width and one --scheme")
	}
	if height <= 0 {
		return nil, fmt.Errorf("--height %d: must be positive", height)
	}
	var cases []shotCase
	for _, w := range widths {
		if w <= 0 {
			return nil, fmt.Errorf("--width %d: must be positive", w)
		}
		for _, sc := range schemes {
			if sc != "light" && sc != "dark" {
				return nil, fmt.Errorf("--scheme %q: want light or dark", sc)
			}
			cases = append(cases, shotCase{Name: fmt.Sprintf("%d-%s", w, sc), Width: w, Height: height, Scheme: sc})
		}
	}
	return cases, nil
}

// shotReport is metrics.json.
type shotReport struct {
	Fixture string       `json:"fixture"`
	URL     string       `json:"url"`
	Browser string       `json:"browser"`
	Build   shotBuild    `json:"build"`
	Cases   []shotResult `json:"cases"`
}

type shotBuild struct {
	Dir string   `json:"dir"`
	CSS []string `json:"css"`
	JS  []string `json:"js"`
}

// shotResult is what one case captured.
type shotResult struct {
	shotCase
	Screenshot string          `json:"screenshot"`
	DOM        string          `json:"dom"`
	Metrics    json.RawMessage `json:"metrics"`
	Eval       json.RawMessage `json:"eval,omitempty"`
	EvalError  string          `json:"eval_error,omitempty"`
	Errors     []string        `json:"errors"` // console errors and uncaught exceptions
}

func webShot(cmd *cobra.Command, s shotSettings) error {
	cases, err := shotCases(s.widths, s.height, s.schemes)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(s.path, "/") {
		return fmt.Errorf("--path %q: must start with /", s.path)
	}
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not inside a git checkout")
	}
	webDir := filepath.Join(top, "web")
	if _, err := os.Stat(filepath.Join(webDir, "package.json")); err != nil {
		return fmt.Errorf("no web UI in %s: web/package.json is missing", top)
	}
	fixtures := filepath.Join(webDir, "fixtures")
	if _, err := webui.LoadFixture(fixtures, s.fixture); err != nil {
		return err
	}
	// Find the browser before the build, so its absence fails fast.
	browser, err := findBrowser(s.browser)
	if err != nil {
		return err
	}

	out := s.out
	if out == "" {
		if out, err = os.MkdirTemp("", "bottown-shot-"); err != nil {
			return err
		}
	} else if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if out, err = filepath.Abs(out); err != nil {
		return err
	}

	ctx, errOut := cmd.Context(), cmd.ErrOrStderr()
	if err := npmInstalled(ctx, webDir, errOut); err != nil {
		return err
	}
	buildDir := filepath.Join(out, "build")
	if err := viteBuild(ctx, webDir, buildDir, errOut); err != nil {
		return err
	}

	srv, err := webui.NewPreviewServer(webui.PreviewConfig{
		UI:          os.DirFS(buildDir),
		FixturesDir: fixtures,
		Fixture:     s.fixture,
	})
	if err != nil {
		return err
	}
	serveCtx, stopServe := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()
	defer func() {
		stopServe()
		<-served
		_ = srv.Close()
	}()

	report := shotReport{Fixture: s.fixture, URL: srv.BaseURL() + s.path, Browser: browser, Build: builtAssets(buildDir)}
	fmt.Fprintf(errOut, "capturing %s with %s: %d case(s)\n", report.URL, browser, len(cases))
	capCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	report.Cases, err = capture(capCtx, browser, report.URL, cases, captureOptions{
		selectors: s.selectors,
		eval:      s.eval,
		wait:      s.wait,
		out:       out,
		errOut:    errOut,
	})
	if err != nil {
		return err
	}

	metrics := filepath.Join(out, "metrics.json")
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(metrics, append(b, '\n'), 0o644); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, metrics)
	for _, r := range report.Cases {
		fmt.Fprintln(w, r.Screenshot)
		fmt.Fprintln(w, r.DOM)
		for _, e := range r.Errors {
			fmt.Fprintf(errOut, "%s: page error: %s\n", r.Name, e)
		}
		if r.EvalError != "" {
			fmt.Fprintf(errOut, "%s: --eval: %s\n", r.Name, r.EvalError)
		}
	}
	for _, p := range append(report.Build.CSS, report.Build.JS...) {
		fmt.Fprintln(w, p)
	}
	return nil
}

// viteBuild builds the web UI in webDir into out, once. It runs vite itself
// rather than npm run, so that cancelling ctx stops it.
func viteBuild(ctx context.Context, webDir, out string, errOut io.Writer) error {
	vite := exec.CommandContext(ctx, filepath.Join(webDir, "node_modules", ".bin", "vite"), "build", "--outDir", out, "--emptyOutDir")
	vite.Dir, vite.Stdout, vite.Stderr = webDir, errOut, errOut
	if err := vite.Run(); err != nil {
		return fmt.Errorf("build the web UI: %w", err)
	}
	return nil
}

// builtAssets lists the build's CSS and JS files.
func builtAssets(dir string) shotBuild {
	b := shotBuild{Dir: dir, CSS: []string{}, JS: []string{}}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch filepath.Ext(p) {
		case ".css":
			b.CSS = append(b.CSS, p)
		case ".js":
			b.JS = append(b.JS, p)
		}
		return nil
	})
	sort.Strings(b.CSS)
	sort.Strings(b.JS)
	return b
}

// browserCandidates are where Chrome, Chromium and Edge are usually
// installed, by platform, then the names to look for on PATH.
func browserCandidates() []string {
	var c []string
	if runtime.GOOS == "darwin" {
		apps := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		}
		home, _ := os.UserHomeDir()
		for _, a := range apps {
			c = append(c, filepath.Join("/Applications", a))
			if home != "" {
				c = append(c, filepath.Join(home, "Applications", a))
			}
		}
	}
	return append(c, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"chrome", "chrome-headless-shell", "microsoft-edge")
}

// findBrowser returns the browser to drive: the flag, else $BOTTOWN_BROWSER,
// else the first installed candidate.
func findBrowser(flag string) (string, error) {
	for _, explicit := range []struct{ value, from string }{{flag, "--browser"}, {os.Getenv(browserEnv), "$" + browserEnv}} {
		if explicit.value == "" {
			continue
		}
		p, err := exec.LookPath(explicit.value)
		if err != nil {
			return "", fmt.Errorf("%s %s: not an executable: %w", explicit.from, explicit.value, err)
		}
		return p, nil
	}
	for _, c := range browserCandidates() {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Chrome or Chromium found: install one, or name it with --browser or $%s; `web preview --detach` still serves the page to open by hand", browserEnv)
}

// captureOptions are what every case measures and where it writes.
type captureOptions struct {
	selectors []string
	eval      string
	wait      string
	out       string
	errOut    io.Writer
}

// capture starts the browser and captures each case of url into o.out.
func capture(ctx context.Context, browser, url string, cases []shotCase, o captureOptions) ([]shotResult, error) {
	b, err := startBrowser(ctx, browser)
	if err != nil {
		return nil, err
	}
	defer b.close()
	p, err := b.newPage(ctx)
	if err != nil {
		return nil, err
	}
	var results []shotResult
	for _, c := range cases {
		r, err := p.capture(ctx, url, c, o)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		fmt.Fprintf(o.errOut, "captured %s\n", c.Name)
		results = append(results, r)
	}
	return results, nil
}

// headless is a browser started for one capture, in a profile of its own.
type headless struct {
	cmd     *exec.Cmd
	profile string
	log     string
	exited  chan struct{}
	conn    *cdpConn

	mu    sync.Mutex
	pages map[string]*shotPage // by CDP session
}

// startBrowser launches the browser headless with a fresh profile, waits
// for its DevTools endpoint, and connects.
func startBrowser(ctx context.Context, path string) (*headless, error) {
	profile, err := os.MkdirTemp("", "bottown-shot-profile-")
	if err != nil {
		return nil, err
	}
	// The log sits beside the browser's own profile, data/.
	b := &headless{profile: profile, log: filepath.Join(profile, "browser.log"), exited: make(chan struct{}), pages: map[string]*shotPage{}}
	logf, err := os.Create(b.log)
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, err
	}
	args := []string{
		"--headless=new",
		"--user-data-dir=" + filepath.Join(profile, "data"),
		"--remote-debugging-port=0",
		"--remote-allow-origins=http://127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--mute-audio",
		"about:blank",
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	b.cmd = exec.Command(path, args...)
	b.cmd.Stdout, b.cmd.Stderr = logf, logf
	if err := b.cmd.Start(); err != nil {
		logf.Close()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start the browser %s: %w (a sandboxed run may not be allowed to; `web preview --detach` serves the page for someone to open)", path, err)
	}
	go func() { _ = b.cmd.Wait(); logf.Close(); close(b.exited) }()

	wsURL, err := b.awaitDevTools(ctx, filepath.Join(profile, "data", "DevToolsActivePort"))
	if err == nil {
		b.conn, err = dialCDP(wsURL, b.dispatch)
	}
	if err != nil {
		log := tailFile(b.log, 20)
		b.close()
		if log != "" {
			err = fmt.Errorf("%w; the browser said:\n%s", err, log)
		}
		return nil, fmt.Errorf("drive the browser %s: %w", path, err)
	}
	return b, nil
}

// awaitDevTools waits for the browser to write the port and path of its
// DevTools endpoint.
func (b *headless) awaitDevTools(ctx context.Context, portFile string) (string, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(portFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == 2 {
				if _, err := strconv.Atoi(lines[0]); err == nil {
					return "ws://127.0.0.1:" + lines[0] + lines[1], nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for its DevTools endpoint: %w", ctx.Err())
		case <-b.exited:
			return "", fmt.Errorf("it exited (%s) before it opened its DevTools endpoint (a sandboxed run may not be allowed to start it; `web preview --detach` serves the page for someone to open)", b.cmd.ProcessState)
		case <-tick.C:
		}
	}
}

// dispatch hands a page's events to it.
func (b *headless) dispatch(m cdpMessage) {
	b.mu.Lock()
	p := b.pages[m.SessionID]
	b.mu.Unlock()
	if p != nil {
		p.event(m)
	}
}

// close asks the browser to exit, kills it if it does not, and removes its
// profile.
func (b *headless) close() {
	if b.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = b.conn.call(ctx, "", "Browser.close", nil, nil)
		cancel()
		_ = b.conn.Close()
	}
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
		<-b.exited
	}
	_ = os.RemoveAll(b.profile)
}

// shotPage is a page target, attached as a flattened session. It tracks
// what it needs to tell when a load has settled, and the page's errors.
type shotPage struct {
	b       *headless
	session string

	mu       sync.Mutex
	loads    map[string]bool // the loaders whose load event fired
	inflight map[string]bool
	activity time.Time
	errors   []string
}

func (b *headless) newPage(ctx context.Context) (*shotPage, error) {
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return nil, err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &att); err != nil {
		return nil, err
	}
	p := &shotPage{b: b, session: att.SessionID, loads: map[string]bool{}, inflight: map[string]bool{}}
	b.mu.Lock()
	b.pages[att.SessionID] = p
	b.mu.Unlock()
	for _, m := range []string{"Page.enable", "Network.enable", "Runtime.enable", "Log.enable"} {
		if err := p.call(ctx, m, nil, nil); err != nil {
			return nil, err
		}
	}
	if err := p.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *shotPage) call(ctx context.Context, method string, params, result any) error {
	return p.b.conn.call(ctx, p.session, method, params, result)
}

func (p *shotPage) event(m cdpMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		LoaderID  string `json:"loaderId"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Args      []struct {
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"args"`
		ExceptionDetails struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
		Entry struct {
			Level string `json:"level"`
			Text  string `json:"text"`
			URL   string `json:"url"`
		} `json:"entry"`
	}
	_ = json.Unmarshal(m.Params, &ev)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m.Method {
	case "Page.lifecycleEvent":
		if ev.Name == "load" {
			p.loads[ev.LoaderID] = true
			p.activity = time.Now()
		}
	case "Network.requestWillBeSent":
		p.inflight[ev.RequestID] = true
		p.activity = time.Now()
	case "Network.loadingFinished", "Network.loadingFailed":
		delete(p.inflight, ev.RequestID)
		p.activity = time.Now()
	case "Runtime.consoleAPICalled":
		if ev.Type == "error" || ev.Type == "assert" {
			var parts []string
			for _, a := range ev.Args {
				if a.Description != "" {
					parts = append(parts, a.Description)
				} else {
					parts = append(parts, fmt.Sprint(a.Value))
				}
			}
			p.errors = append(p.errors, "console."+ev.Type+": "+strings.Join(parts, " "))
		}
	case "Runtime.exceptionThrown":
		msg := ev.ExceptionDetails.Exception.Description
		if msg == "" {
			msg = ev.ExceptionDetails.Text
		}
		p.errors = append(p.errors, "uncaught: "+msg)
	case "Log.entryAdded":
		if ev.Entry.Level == "error" {
			p.errors = append(p.errors, ev.Entry.Text+" "+ev.Entry.URL)
		}
	}
}

// settleQuiet is how long the network must be idle after the load event
// for a page to count as rendered: the UI renders what its fetches return.
const settleQuiet = 500 * time.Millisecond

// capture loads url at the case's size and scheme, waits for it to settle,
// and writes its screenshot and DOM.
func (p *shotPage) capture(ctx context.Context, url string, c shotCase, o captureOptions) (shotResult, error) {
	r := shotResult{shotCase: c, Errors: []string{}}
	if err := p.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": c.Width, "height": c.Height, "deviceScaleFactor": 1, "mobile": false,
	}, nil); err != nil {
		return r, err
	}
	if err := p.call(ctx, "Emulation.setEmulatedMedia", map[string]any{
		"features": []map[string]string{{"name": "prefers-color-scheme", "value": c.Scheme}},
	}, nil); err != nil {
		return r, err
	}
	// From a blank page, so the same URL loads again rather than only
	// moving to its fragment.
	if err := p.call(ctx, "Page.navigate", map[string]any{"url": "about:blank"}, nil); err != nil {
		return r, err
	}
	p.mu.Lock()
	p.inflight, p.errors, p.activity = map[string]bool{}, nil, time.Now()
	p.mu.Unlock()
	var nav struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := p.call(ctx, "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
		return r, err
	}
	if nav.ErrorText != "" {
		return r, fmt.Errorf("load %s: %s", url, nav.ErrorText)
	}
	if err := p.settle(ctx, nav.LoaderID); err != nil {
		return r, err
	}
	if o.wait != "" {
		if err := p.waitFor(ctx, o.wait); err != nil {
			return r, err
		}
	}

	opts, _ := json.Marshal(map[string]any{"selectors": o.selectors})
	if err := p.evaluate(ctx, "("+shotMetricsJS+")("+string(opts)+")", &r.Metrics); err != nil {
		return r, fmt.Errorf("measure the page: %w", err)
	}
	if o.eval != "" {
		if err := p.evaluate(ctx, o.eval, &r.Eval); err != nil {
			r.EvalError = err.Error()
		}
	}
	var dom string
	if err := p.evaluate(ctx, `(document.doctype ? new XMLSerializer().serializeToString(document.doctype) + "\n" : "") + document.documentElement.outerHTML`, &dom); err != nil {
		return r, fmt.Errorf("read the DOM: %w", err)
	}
	r.DOM = filepath.Join(o.out, c.Name+".dom.html")
	if err := os.WriteFile(r.DOM, []byte(dom+"\n"), 0o644); err != nil {
		return r, err
	}
	png, err := p.screenshot(ctx, c)
	if err != nil {
		return r, err
	}
	r.Screenshot = filepath.Join(o.out, c.Name+".png")
	if err := os.WriteFile(r.Screenshot, png, 0o644); err != nil {
		return r, err
	}
	p.mu.Lock()
	r.Errors = append(r.Errors, p.errors...)
	p.mu.Unlock()
	return r, nil
}

// settle waits for the navigation's load event, and then for the network
// to stay idle.
func (p *shotPage) settle(ctx context.Context, loader string) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		p.mu.Lock()
		done := p.loads[loader] && len(p.inflight) == 0 && time.Since(p.activity) >= settleQuiet
		p.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the page did not settle: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// waitFor polls until selector matches an element.
func (p *shotPage) waitFor(ctx context.Context, selector string) error {
	sel, _ := json.Marshal(selector)
	for {
		var found bool
		if err := p.evaluate(ctx, "!!document.querySelector("+string(sel)+")", &found); err != nil {
			return fmt.Errorf("--wait %s: %w", selector, err)
		}
		if found {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("--wait %s: no element matched: %w", selector, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// evaluate runs expr in the page, awaiting a promise, and decodes its JSON
// value into v.
func (p *shotPage) evaluate(ctx context.Context, expr string, v any) error {
	var res struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": true,
	}, &res); err != nil {
		return err
	}
	if e := res.ExceptionDetails; e != nil {
		if e.Exception.Description != "" {
			return errors.New(e.Exception.Description)
		}
		return errors.New(e.Text)
	}
	raw := res.Result.Value
	if len(raw) == 0 {
		raw = json.RawMessage("null") // undefined
	}
	if rm, ok := v.(*json.RawMessage); ok {
		*rm = append(json.RawMessage(nil), raw...)
		return nil
	}
	return json.Unmarshal(raw, v)
}

// maxShotHeight caps a full-page screenshot, so a runaway page cannot
// make an unbounded image.
const maxShotHeight = 16384

// screenshot captures the whole page, past the viewport, as a PNG.
func (p *shotPage) screenshot(ctx context.Context, c shotCase) ([]byte, error) {
	var lm struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := p.call(ctx, "Page.getLayoutMetrics", nil, &lm); err != nil {
		return nil, err
	}
	w, h := max(float64(c.Width), lm.CSSContentSize.Width), max(float64(c.Height), lm.CSSContentSize.Height)
	h = min(h, maxShotHeight)
	var shot struct {
		Data string `json:"data"`
	}
	if err := p.call(ctx, "Page.captureScreenshot", map[string]any{
		"format": "png", "captureBeyondViewport": true,
		"clip": map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": 1},
	}, &shot); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}
