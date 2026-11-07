package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestParseRetroSince(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want retroSince
	}{
		{"last", retroSince{Last: true}},
		{"git:v1.2", retroSince{Ref: "v1.2"}},
		{"git:abc1234", retroSince{Ref: "abc1234"}},
		{"2026-10-24", retroSince{Before: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}},
		{"3d", retroSince{Before: now.Add(-72 * time.Hour)}},
		{"2w", retroSince{Before: now.Add(-14 * 24 * time.Hour)}},
	} {
		got, err := parseRetroSince(c.in, now)
		if err != nil || got != c.want {
			t.Errorf("parseRetroSince(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "git:", "git:-x", "git:a b", "0w", "2m", "yesterday", "2026-13-01"} {
		if _, err := parseRetroSince(bad, now); err == nil {
			t.Errorf("parseRetroSince(%q) = nil error, want one", bad)
		}
	}
}

// windowRepo is a Forgejo repository whose main has three first-parent
// commits, a day apart, the last a merge of issue/1; and a wiki.
type windowRepo struct {
	root, gitDir, wikiDir string
	c1, c2, side, merge   string
}

func newWindowRepo(t *testing.T) windowRepo {
	t.Helper()
	root := t.TempDir()
	r := windowRepo{root: root, gitDir: filepath.Join(root, "bottown", "demo.git"), wikiDir: filepath.Join(root, "bottown", "demo.wiki.git")}
	work := t.TempDir()
	var date string
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(day, file string) string {
		t.Helper()
		date = day + "T12:00:00Z"
		if err := os.WriteFile(filepath.Join(work, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		git(work, "add", file)
		git(work, "commit", "-q", "-m", file)
		return git(work, "rev-parse", "HEAD")
	}
	git(work, "init", "-q", "-b", "main")
	r.c1 = commit("2026-11-01", "one")
	r.c2 = commit("2026-11-02", "two")
	git(work, "checkout", "-q", "-b", "issue/1")
	r.side = commit("2026-11-02", "side")
	git(work, "checkout", "-q", "main")
	date = "2026-11-03T12:00:00Z"
	git(work, "merge", "-q", "--no-ff", "-m", "Merge issue/1", "issue/1")
	r.merge = git(work, "rev-parse", "HEAD")
	git(work, "tag", "v1", r.c2)
	git(work, "init", "-q", "--bare", r.gitDir)
	git(work, "push", "-q", r.gitDir, "main", "issue/1", "v1")
	return r
}

// wikiPages commits pages (name -> text) to the repository's wiki.
func (r windowRepo) wikiPages(t *testing.T, pages map[string]string) {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for name, text := range pages {
		if err := os.WriteFile(filepath.Join(work, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "pages")
	_ = os.RemoveAll(r.wikiDir)
	run("init", "-q", "--bare", "-b", "main", r.wikiDir)
	run("push", "-q", r.wikiDir, "main")
}

func TestRetroWindow(t *testing.T) {
	r := newWindowRepo(t)
	s := &Server{GitRoot: r.root, Log: slog.Default()}
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	window := func(since string) (*secrets.RetroWindow, error) {
		return s.retroWindow(ctx, "bottown", "demo", "main", r.merge, since, now)
	}
	status := func(err error) int {
		var re *runError
		if errors.As(err, &re) {
			return re.status
		}
		return 0
	}

	for since, base := range map[string]string{
		"git:" + r.c1:     r.c1,
		"git:" + r.c1[:8]: r.c1,
		"git:v1":          r.c2,
		"git:main~1":      r.c2,
		// main as it was at the start of the 2nd (UTC): the 1st's commit.
		"2026-11-02": r.c1,
		// Five days before the 7th, 09:00: the 1st's commit, as the
		// 2nd's is from noon.
		"5d": r.c1,
	} {
		w, err := window(since)
		if err != nil {
			t.Errorf("window --since %s: %v", since, err)
			continue
		}
		short := strings.TrimSpace(mustGit(t, r.gitDir, "rev-parse", "--short", r.merge))
		if w.Base != base || w.Since != since || w.Page != "20261107-"+short+"-retrospective" {
			t.Errorf("window --since %s = %+v, want base %s and page 20261107-%s-retrospective", since, w, base, short)
		}
	}
	for since, want := range map[string]int{
		"git:" + r.side:  http.StatusConflict, // on issue/1, not on main
		"git:" + r.merge: http.StatusConflict, // nothing landed after the head
		"git:nope":       http.StatusNotFound,
		"2026-10-01":     http.StatusNotFound, // before main's first commit
		"last":           http.StatusNotFound, // no wiki yet
		"soon":           http.StatusBadRequest,
	} {
		if _, err := window(since); status(err) != want {
			t.Errorf("window --since %s: %v (status %d), want status %d", since, err, status(err), want)
		}
	}

	// last: the windowed retro page whose head is furthest along main,
	// whatever its date; pages whose head is not on main are skipped.
	r.wikiPages(t, map[string]string{
		"Home.md": "home",
		"20261105-" + r.c2[:9] + "-retrospective.md":   "newer",
		"20261106-" + r.c1[:7] + "-retrospective.md":   "a later rerun of an older window",
		"20261106-" + r.side[:7] + "-retrospective.md": "off main",
		"20261101-zzzzzzz-retrospective.md":            "not a sha",
	})
	if w, err := window("last"); err != nil || w.Base != r.c2 {
		t.Errorf("window --since last = %+v, %v; want base %s", w, err, r.c2)
	}

	if _, err := (&Server{Log: slog.Default()}).retroWindow(ctx, "bottown", "demo", "main", r.merge, "last", now); status(err) != http.StatusNotImplemented {
		t.Errorf("window without git repositories: %v, want 501", err)
	}
}

func mustGit(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	out, err := mergeGit(context.Background(), gitDir, nil, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A windowed retro run is the owner's, on no issue, at main's head, with
// its window and page recorded, and no family rule; it writes its own
// wiki page and no other.
func TestWindowedRetroRun(t *testing.T) {
	r := newWindowRepo(t)
	ts := newRetroTestServer(t, r.merge)
	ts.srv.GitRoot = r.root

	if _, w := ts.create(ts.ownerKey, `{"kind":"retro","actor":"claude","repo":"demo","since":"git:`+r.c1+`","issue":7}`); w.Code != http.StatusBadRequest {
		t.Errorf("windowed retro with an issue = %d (%s), want 400", w.Code, w.Body.String())
	}
	if _, w := ts.create(ts.ownerKey, `{"kind":"discuss","actor":"claude","repo":"demo","since":"git:`+r.c1+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("windowed discuss = %d (%s), want 400", w.Code, w.Body.String())
	}
	if _, w := ts.create(ts.ownerKey, `{"kind":"retro","actor":"claude","repo":"demo","since":"git:`+r.c1+`","allow_same_family":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("windowed retro with allow_same_family = %d (%s), want 400", w.Code, w.Body.String())
	}
	if _, w := ts.create(ts.actorKey, `{"kind":"retro","repo":"demo","since":"git:`+r.c1+`"}`); w.Code != http.StatusForbidden {
		t.Errorf("self-started windowed retro = %d (%s), want 403", w.Code, w.Body.String())
	}
	// claude implemented #7, which a window may well include: no matter.
	created, w := ts.create(ts.ownerKey, `{"kind":"retro","actor":"claude","repo":"demo","since":"git:v1"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("windowed retro = %d (%s), want 201", w.Code, w.Body.String())
	}
	if created.Issue != 0 || created.SHA != r.merge || created.Window == nil || created.Window.Base != r.c2 ||
		created.Window.Since != "git:v1" || !strings.HasSuffix(created.Window.Page, "-retrospective") || created.AllowSameFamily {
		t.Fatalf("windowed retro run = %+v (window %+v), want main's head and the window after v1", created.Run, created.Window)
	}
	// A second one on the same page would clobber the first.
	if _, w := ts.create(ts.ownerKey, `{"kind":"retro","actor":"codex","repo":"demo","since":"git:v1"}`); w.Code != http.StatusConflict {
		t.Errorf("second windowed retro on the same page = %d (%s), want 409", w.Code, w.Body.String())
	}

	p, ok := ts.srv.Lookup(created.Key)
	if !ok || p.Run == nil {
		t.Fatal("the run key is unknown")
	}
	st := &wikiState{}
	for file, want := range map[string]bool{
		created.Window.Page + ".md":         true,
		"Home.md":                           false,
		"20990101-abcdef0-retrospective.md": false,
	} {
		if d := ts.srv.wikiDecide(p, st, true, file); d.Allow != want {
			t.Errorf("windowed retro writes %s: %+v, want allowed %v", file, d, want)
		}
	}
	if d := ts.srv.wikiDecide(p, st, false, "Home.md"); !d.Allow {
		t.Errorf("windowed retro reads Home.md: %+v, want allowed", d)
	}
	plain := p
	plain.Run = &secrets.Run{ID: "r-x", Kind: policy.Retro, Issue: 7}
	if d := ts.srv.wikiDecide(plain, st, true, created.Window.Page+".md"); d.Allow {
		t.Errorf("a retro of an issue writes the window's page: %+v, want refused", d)
	}
}

// retroTestServer is a server on a fake Forgejo whose main is at head,
// where claude implemented #7, with an owner key and claude's actor key.
type retroTestServer struct {
	srv                *Server
	ownerKey, actorKey string
	create             func(key, body string) (RunResponse, *httpRecorder)
}

type httpRecorder = httptest.ResponseRecorder

// fakeForge is a Forgejo that answers "METHOD /path" with the JSON in
// answers, and fails the test on any other request.
func fakeForge(t *testing.T, answers map[string]string) *url.URL {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// postRun creates a run with key through h.
func postRun(t *testing.T, h http.Handler, key, body string) (RunResponse, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var created RunResponse
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
	}
	return created, w
}

func newRetroTestServer(t *testing.T, head string) retroTestServer {
	t.Helper()
	upstream := fakeForge(t, map[string]string{
		"GET /api/v1/repos/bottown/demo":               `{"name":"demo","default_branch":"main"}`,
		"GET /api/v1/repos/bottown/demo/branches/main": `{"name":"main","commit":{"id":"` + head + `"}}`,
	})
	dir := t.TempDir()
	store, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "codex": {Token: "tok_codex"}}
		st.Runs = map[string]secrets.Run{
			"r-impl": {ID: "r-impl", Kind: policy.Implement, Actor: "claude", Family: "anthropic", Org: "bottown", Repo: "demo", Issue: 7, Branch: "issue/7", Ended: &ended},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ownerKey, err := store.NewAPIKey("owner", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	actorKey, err := store.NewAPIKey("claude", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: upstream, Store: store, Audit: audit,
		Rec: &provision.Reconciler{BaseURL: upstream.String(), Desired: config.Desired{
			Org: "bottown",
			Actors: []config.Actor{
				{Name: "claude", Family: "anthropic", Harness: "claude"},
				{Name: "codex", Family: "openai", Harness: "codex"},
			},
		}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	srv.health.Reconciled = true
	return retroTestServer{srv: srv, ownerKey: ownerKey, actorKey: actorKey, create: func(key, body string) (RunResponse, *httpRecorder) {
		t.Helper()
		return postRun(t, srv.Handler(), key, body)
	}}
}
