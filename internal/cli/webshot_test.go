package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestShotCases(t *testing.T) {
	cases, err := shotCases([]int{1440, 390}, 900, []string{"light", "dark"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cases {
		names = append(names, c.Name)
		if c.Height != 900 {
			t.Errorf("%s: height %d", c.Name, c.Height)
		}
	}
	if got := strings.Join(names, " "); got != "1440-light 1440-dark 390-light 390-dark" {
		t.Errorf("cases: %s", got)
	}
	for _, bad := range []struct {
		widths  []int
		height  int
		schemes []string
	}{
		{nil, 900, []string{"light"}},
		{[]int{100}, 900, nil},
		{[]int{0}, 900, []string{"light"}},
		{[]int{100}, 0, []string{"light"}},
		{[]int{100}, 900, []string{"sepia"}},
	} {
		if _, err := shotCases(bad.widths, bad.height, bad.schemes); err == nil {
			t.Errorf("shotCases(%v, %d, %v): no error", bad.widths, bad.height, bad.schemes)
		}
	}
}

func TestFindBrowser(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "chrome")
	writeFile(t, exe, "#!/bin/sh\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(browserEnv, "")
	if got, err := findBrowser(exe); err != nil || got != exe {
		t.Errorf("--browser: %q %v", got, err)
	}
	if _, err := findBrowser(filepath.Join(t.TempDir(), "absent")); err == nil || !strings.Contains(err.Error(), "--browser") {
		t.Errorf("missing --browser: %v", err)
	}
	t.Setenv(browserEnv, exe)
	if got, err := findBrowser(""); err != nil || got != exe {
		t.Errorf("$%s: %q %v", browserEnv, got, err)
	}
	// Nothing installed: a clear message naming the ways out.
	t.Setenv(browserEnv, "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if p, err := findBrowser(""); err == nil {
		if !strings.HasPrefix(p, "/Applications/") {
			t.Errorf("found %q on an empty PATH", p)
		}
	} else if !strings.Contains(err.Error(), "no Chrome or Chromium") || !strings.Contains(err.Error(), "web preview") {
		t.Errorf("no browser: %v", err)
	}
}

// fakeCDP is a stand-in for a browser's DevTools endpoint: it answers the
// commands web shot sends, with the events a page load makes.
type fakeCDP struct {
	closed string // created on Browser.close, which ends the fake browser

	mu      sync.Mutex
	methods []string
	schemes []string
	widths  []int
}

func (f *fakeCDP) serve(ws *websocket.Conn) {
	send := func(v any) { _ = websocket.JSON.Send(ws, v) }
	event := func(method string, params any) {
		send(map[string]any{"method": method, "params": params, "sessionId": "S"})
	}
	for {
		var m struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := websocket.JSON.Receive(ws, &m); err != nil {
			return
		}
		var p struct {
			URL        string `json:"url"`
			Expression string `json:"expression"`
			Width      int    `json:"width"`
			Features   []struct{ Value string }
		}
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.methods = append(f.methods, m.Method)
		f.mu.Unlock()
		var result any = map[string]any{}
		switch m.Method {
		case "Target.createTarget":
			result = map[string]any{"targetId": "T"}
		case "Target.attachToTarget":
			result = map[string]any{"sessionId": "S"}
		case "Emulation.setDeviceMetricsOverride":
			f.mu.Lock()
			f.widths = append(f.widths, p.Width)
			f.mu.Unlock()
		case "Emulation.setEmulatedMedia":
			f.mu.Lock()
			f.schemes = append(f.schemes, p.Features[0].Value)
			f.mu.Unlock()
		case "Page.navigate":
			if p.URL == "about:blank" {
				result = map[string]any{"loaderId": "blank"}
				break
			}
			event("Network.requestWillBeSent", map[string]any{"requestId": "1"})
			event("Network.loadingFinished", map[string]any{"requestId": "1"})
			event("Page.lifecycleEvent", map[string]any{"loaderId": "L", "name": "load"})
			event("Runtime.consoleAPICalled", map[string]any{"type": "error", "args": []any{map[string]any{"value": "boom"}}})
			result = map[string]any{"loaderId": "L"}
		case "Runtime.evaluate":
			var v any
			switch {
			case strings.Contains(p.Expression, "outerHTML"):
				v = "<html><h2>Owner Inbox</h2></html>"
			case strings.Contains(p.Expression, "selectors"):
				v = map[string]any{"order": []string{"Owner Inbox"}}
			case p.Expression == "6*7":
				v = 42
			default:
				result = map[string]any{"result": map[string]any{"type": "object"},
					"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "ReferenceError: nope"}}}
			}
			if v != nil {
				result = map[string]any{"result": map[string]any{"type": "object", "value": v}}
			}
		case "Page.getLayoutMetrics":
			result = map[string]any{"cssContentSize": map[string]any{"width": 100, "height": 200}}
		case "Page.captureScreenshot":
			result = map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("PNG"))}
		case "Browser.close":
			_ = os.WriteFile(f.closed, nil, 0o600)
		}
		send(map[string]any{"id": m.ID, "result": result})
	}
}

// fakeBrowser writes a script that plays a browser: it records the fake
// DevTools endpoint where Chrome would, and runs until Browser.close.
func fakeBrowser(t *testing.T, port int, closed string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "fake-chrome")
	writeFile(t, exe, `#!/bin/sh
for a in "$@"; do
  case "$a" in --user-data-dir=*) dir="${a#--user-data-dir=}" ;; esac
done
mkdir -p "$dir"
printf '`+strconv.Itoa(port)+`\n/devtools/browser/fake\n' > "$dir/DevToolsActivePort"
while [ ! -e '`+closed+`' ]; do sleep 0.05; done
`)
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// TestWebShot runs `web shot` end to end, with stand-ins for vite and for
// the browser.
func TestWebShot(t *testing.T) {
	top := fakeCheckout(t)
	t.Chdir(top)
	t.Setenv("BOTTOWN_INSTANCE", "")

	f := &fakeCDP{closed: filepath.Join(t.TempDir(), "closed")}
	srv := httptest.NewServer(websocket.Handler(f.serve))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv(browserEnv, fakeBrowser(t, port, f.closed))

	out := filepath.Join(t.TempDir(), "shot")
	root := newRoot()
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs([]string{"web", "shot", "--out", out, "--width", "800,300", "--scheme", "dark", "--eval", "6*7", "--timeout", "20s"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("web shot: %v\n%s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	want := []string{"metrics.json", "800-dark.png", "800-dark.dom.html", "300-dark.png", "300-dark.dom.html"}
	if len(lines) != len(want) {
		t.Fatalf("output:\n%s", stdout.String())
	}
	for i, w := range want {
		if lines[i] != filepath.Join(out, w) {
			t.Errorf("line %d: %q, want %s", i, lines[i], filepath.Join(out, w))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "800-dark.png")); string(b) != "PNG" {
		t.Errorf("screenshot: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "300-dark.dom.html")); !strings.Contains(string(b), "<h2>Owner Inbox</h2>") {
		t.Errorf("dom: %q", b)
	}
	if _, err := os.Stat(filepath.Join(out, "build", "index.html")); err != nil {
		t.Errorf("build: %v", err)
	}

	var report shotReport
	b, err := os.ReadFile(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if report.Fixture != "default" || !strings.HasPrefix(report.URL, "http://127.0.0.1:") || len(report.Cases) != 2 {
		t.Fatalf("report: %+v", report)
	}
	c := report.Cases[0]
	if c.Name != "800-dark" || c.Width != 800 || c.Scheme != "dark" {
		t.Errorf("case: %+v", c.shotCase)
	}
	var metrics bytes.Buffer
	_ = json.Compact(&metrics, c.Metrics)
	if metrics.String() != `{"order":["Owner Inbox"]}` || string(c.Eval) != "42" {
		t.Errorf("metrics %s, eval %s", c.Metrics, c.Eval)
	}
	if len(c.Errors) != 1 || !strings.Contains(c.Errors[0], "boom") {
		t.Errorf("errors: %v", c.Errors)
	}
	if !strings.Contains(stderr.String(), "page error: console.error: boom") {
		t.Errorf("stderr does not report the page error:\n%s", stderr.String())
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.schemes, ","); got != "dark,dark" {
		t.Errorf("schemes: %s", got)
	}
	if len(f.widths) != 2 || f.widths[0] != 800 || f.widths[1] != 300 {
		t.Errorf("widths: %v", f.widths)
	}
	if f.methods[len(f.methods)-1] != "Browser.close" {
		t.Errorf("the browser was not closed: %v", f.methods)
	}
}

// TestWebShotBrowserFails reports a browser that will not start, with
// what it said.
func TestWebShotBrowserFails(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "chrome")
	writeFile(t, exe, "#!/bin/sh\necho 'sandbox: operation not permitted' >&2\nexit 1\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := capture(ctx, exe, "http://127.0.0.1:1/", []shotCase{{Name: "x", Width: 10, Height: 10, Scheme: "light"}}, captureOptions{out: t.TempDir(), errOut: &lockedBuffer{}})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"exited", "operation not permitted", "web preview --detach"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// TestCaptureRealBrowser measures a page in an installed Chrome or
// Chromium; it is skipped where there is none, or where it may not start.
func TestCaptureRealBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	browser, err := findBrowser("")
	if err != nil {
		t.Skip(err)
	}
	page := `<!doctype html><title>t</title>
<style>
  .list { max-height: 100px; overflow-y: auto; }
  .row { height: 40px; }
  .wide { width: 2000px; height: 10px; }
  @media (prefers-color-scheme: dark) { body { background: rgb(0, 0, 0); } }
</style>
<section><h2>First</h2><div class="list"><div class="row"></div><div class="row"></div><div class="row"></div></div></section>
<section><h2>Second</h2><div class="wide"></div></section>
<script>fetch('/data').then(r => r.text()).then(t => { const p = document.createElement('p'); p.id = 'late'; p.textContent = t; document.body.append(p); });</script>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(page)) })
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("fetched"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out := t.TempDir()
	results, err := capture(ctx, browser, srv.URL+"/", []shotCase{{Name: "400-dark", Width: 400, Height: 300, Scheme: "dark"}},
		captureOptions{selectors: []string{"#late"}, eval: "document.querySelector('#late').textContent", out: out, errOut: &lockedBuffer{}})
	if err != nil {
		if strings.Contains(err.Error(), "drive the browser") || strings.Contains(err.Error(), "start the browser") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	var m struct {
		Scheme string   `json:"scheme"`
		Order  []string `json:"order"`
		Page   struct {
			OverflowX bool `json:"overflowX"`
		} `json:"page"`
		Scrollers []struct {
			El      string `json:"el"`
			Panel   string `json:"panel"`
			ScrollH int    `json:"scrollH"`
			ClientH int    `json:"clientH"`
			MaxH    string `json:"maxH"`
			ScrollY bool   `json:"scrollsY"`
		} `json:"scrollers"`
		OverflowX []struct {
			El string `json:"el"`
		} `json:"overflowX"`
		Selected map[string][]json.RawMessage `json:"selected"`
	}
	r := results[0]
	if err := json.Unmarshal(r.Metrics, &m); err != nil {
		t.Fatalf("%v: %s", err, r.Metrics)
	}
	if m.Scheme != "dark" || strings.Join(m.Order, ",") != "First,Second" {
		t.Errorf("scheme %q, order %v", m.Scheme, m.Order)
	}
	if len(m.Scrollers) != 1 || m.Scrollers[0].El != "div.list" || m.Scrollers[0].Panel != "First" ||
		m.Scrollers[0].ScrollH != 120 || m.Scrollers[0].ClientH != 100 || m.Scrollers[0].MaxH != "100px" || !m.Scrollers[0].ScrollY {
		t.Errorf("scrollers: %+v", m.Scrollers)
	}
	if !m.Page.OverflowX || len(m.OverflowX) != 1 || m.OverflowX[0].El != "div.wide" {
		t.Errorf("overflow: %v %+v", m.Page.OverflowX, m.OverflowX)
	}
	// The capture waited for the page's fetch.
	if len(m.Selected["#late"]) != 1 || string(r.Eval) != `"fetched"` {
		t.Errorf("selected %v, eval %s", m.Selected, r.Eval)
	}
	if b, err := os.ReadFile(r.Screenshot); err != nil || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Errorf("screenshot: %v", err)
	}
	if b, _ := os.ReadFile(r.DOM); !strings.Contains(string(b), `<p id="late">fetched</p>`) {
		t.Errorf("dom: %s", b)
	}
}
