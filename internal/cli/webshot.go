package cli

import (
	"fmt"
	"time"

	"github.com/robertolupi/bottown/internal/webshot"
	"github.com/spf13/cobra"
)

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
	f.StringVar(&s.browser, "browser", "", "the Chrome or Chromium executable (default: $"+webshot.BrowserEnv+", else an installed one)")
	f.DurationVar(&s.timeout, "timeout", 2*time.Minute, "how long the browser may take for all the cases")
	return cmd
}

func webShot(cmd *cobra.Command, s shotSettings) error {
	res, err := webshot.Shoot(cmd.Context(), webshot.Options{
		Fixture:   s.fixture,
		Path:      s.path,
		Widths:    s.widths,
		Height:    s.height,
		Schemes:   s.schemes,
		Selectors: s.selectors,
		Eval:      s.eval,
		Wait:      s.wait,
		Out:       s.out,
		Browser:   s.browser,
		Timeout:   s.timeout,
		ErrOut:    cmd.ErrOrStderr(),
	})
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	for _, p := range res.Paths() {
		fmt.Fprintln(w, p)
	}
	return nil
}
