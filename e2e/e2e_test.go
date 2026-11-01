//go:build e2e

// Package e2e drives the real bottown binary against throwaway instances
// (e2e-<random>), with BOTTOWN_HOME in a temp dir and the file secret store,
// so it never touches prod, dev or the login Keychain. Run with `make e2e`.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/webui"
)

var (
	bin  string // built bottown binary
	home string // BOTTOWN_HOME
	src  string // repo root
)

func TestMain(m *testing.M) {
	// Run inside a bottown run, the environment carries the run's key and
	// prod's URLs (BOTTOWN_RUN_*, BOTTOWN_MCP_URL, ...). Every subprocess
	// inherits os.Environ(), so clear them all: tests set what they need.
	for _, e := range os.Environ() {
		if k, _, _ := strings.Cut(e, "="); strings.HasPrefix(k, "BOTTOWN_") {
			os.Unsetenv(k)
		}
	}
	dir, err := os.MkdirTemp("", "bottown-e2e-")
	if err != nil {
		panic(err)
	}
	_, file, _, _ := runtime.Caller(0)
	src = filepath.Dir(filepath.Dir(file))
	bin = filepath.Join(dir, "bottown")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, filepath.Join(src, "cmd", "bottown")).CombinedOutput(); err != nil {
		panic(string(out))
	}
	home = filepath.Join(dir, "home")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type instance struct {
	t    *testing.T
	name string
	cfg  config.Instance
}

func newInstance(t *testing.T) *instance {
	t.Helper()
	inst := &instance{t: t, name: fmt.Sprintf("e2e-%x", time.Now().UnixNano()&0xffffff)}
	t.Cleanup(func() {
		// Always tear down, even on failure; never leave containers behind.
		if out, err := inst.cmd("", "down", "--purge"); err != nil {
			t.Logf("cleanup %s: %v\n%s", inst.name, err, out)
		}
	})
	return inst
}

func (in *instance) env() []string {
	return append(os.Environ(),
		"BOTTOWN_HOME="+home,
		"BOTTOWN_SECRET_STORE=file",
		"BOTTOWN_OWNER_LOGIN=owner",
		"BOTTOWN_OWNER_EMAIL=owner@e2e.test",
		"BOTTOWN_INSTANCE="+in.name,
	)
}

// cmd runs bottown in dir (src when empty) and returns combined output.
func (in *instance) cmd(dir string, args ...string) (string, error) {
	c := exec.Command(bin, args...)
	c.Env = in.env()
	c.Dir = src
	if dir != "" {
		c.Dir = dir
	}
	out, err := c.CombinedOutput()
	return string(out), err
}

// cmdResolved runs bottown without $BOTTOWN_INSTANCE, so the instance is
// inferred from the checkout in dir.
func (in *instance) cmdResolved(dir string, args ...string) (string, error) {
	c := exec.Command(bin, args...)
	for _, e := range in.env() {
		if !strings.HasPrefix(e, "BOTTOWN_INSTANCE=") {
			c.Env = append(c.Env, e)
		}
	}
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func (in *instance) mustCmd(dir string, args ...string) string {
	in.t.Helper()
	out, err := in.cmd(dir, args...)
	if err != nil {
		in.t.Fatalf("bottown %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (in *instance) up(args ...string) string {
	in.t.Helper()
	out := in.mustCmd("", append([]string{"up", "--timeout", "4m"}, args...)...)
	cfg, ok, err := loadConfig(in.name)
	if err != nil || !ok {
		in.t.Fatalf("no config after up: %v", err)
	}
	in.cfg = cfg
	return out
}

func loadConfig(name string) (config.Instance, bool, error) {
	os.Setenv("BOTTOWN_HOME", home)
	return config.LoadInstance(name)
}

func (in *instance) ownerKey() string {
	in.t.Helper()
	b, err := os.ReadFile(filepath.Join(home, in.name, "host-secrets.json"))
	if err != nil {
		in.t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		in.t.Fatal(err)
	}
	return m["api-key"]
}

// api calls the forge through bottown's proxy with key.
func (in *instance) api(key, method, path string, body any) (int, []byte) {
	in.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, in.cfg.ServerURL()+"/forgejo/api/v1"+path, r)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		in.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (in *instance) mustAPI(key, method, path string, body, out any) {
	in.t.Helper()
	code, b := in.api(key, method, path, body)
	if code < 200 || code > 299 {
		in.t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			in.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func (in *instance) health() map[string]any {
	in.t.Helper()
	resp, err := http.Get(in.cfg.ServerURL() + "/healthz")
	if err != nil {
		in.t.Fatal(err)
	}
	defer resp.Body.Close()
	var h map[string]any
	json.NewDecoder(resp.Body).Decode(&h)
	return h
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=e2e", "-c", "user.email=e2e@x"}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushAs pushes ref to the bottown repo with an actor key, bypassing helpers.
func (in *instance) pushAs(dir, actor, key, refspec string) (string, error) {
	url := fmt.Sprintf("http://%s:%s@127.0.0.1:%d/git/bottown/demo.git", actor, key, in.cfg.ServerPort)
	c := exec.Command("git", "-C", dir, "-c", "credential.helper=", "push", url, refspec)
	out, err := c.CombinedOutput()
	return string(out), err
}

func TestE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	a := newInstance(t)

	t.Run("up from nothing, then idempotent", func(t *testing.T) {
		out := a.up()
		if !strings.Contains(out, "provisioning changes:") {
			t.Fatalf("first up made no changes:\n%s", out)
		}
		if out := a.up(); !strings.Contains(out, "no provisioning changes") {
			t.Fatalf("second up was not a no-op:\n%s", out)
		}
	})
	owner := a.ownerKey()

	t.Run("accounts, org and labels match the spec", func(t *testing.T) {
		for _, u := range []string{"owner", "claude", "codex", "agy", "kimi", "manager"} {
			a.mustAPI(owner, "GET", "/users/"+u, nil, nil)
		}
		var labels []struct{ Name string }
		a.mustAPI(owner, "GET", "/orgs/bottown/labels", nil, &labels)
		got := map[string]bool{}
		for _, l := range labels {
			got[l.Name] = true
		}
		for _, l := range []string{"mode/ask", "mode/auto", "needs/human"} {
			if !got[l] {
				t.Errorf("missing label %s", l)
			}
		}
	})

	// A demo checkout with an external origin.
	// The fam layout: the owner's checkout is <fam>/main.
	fam := t.TempDir()
	demo := filepath.Join(fam, "main")
	os.Mkdir(demo, 0o755)
	git(t, demo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(demo, "README"), []byte("demo\n"), 0o644)
	git(t, demo, "add", ".")
	git(t, demo, "commit", "-q", "-m", "init")
	git(t, demo, "remote", "add", "origin", "https://example.invalid/demo.git")

	t.Run("repo add uses an instance remote and leaves origin alone", func(t *testing.T) {
		out := a.mustCmd(demo, "repo", "add", "demo")
		if !strings.Contains(out, "protected bottown/demo:main") {
			t.Fatalf("repo add did not protect main:\n%s", out)
		}
		if got := git(t, demo, "remote", "get-url", "origin"); got != "https://example.invalid/demo.git" {
			t.Fatalf("origin changed: %s", got)
		}
		want := fmt.Sprintf("http://127.0.0.1:%d/git/bottown/demo.git", a.cfg.ServerPort)
		if got := git(t, demo, "remote", "get-url", "bottown-"+a.name); got != want {
			t.Fatalf("bottown remote = %s, want %s", got, want)
		}
		var bp struct {
			RequiredApprovals     int  `json:"required_approvals"`
			DismissStaleApprovals bool `json:"dismiss_stale_approvals"`
			EnableMergeWhitelist  bool `json:"enable_merge_whitelist"`
		}
		a.mustAPI(owner, "GET", "/repos/bottown/demo/branch_protections/main", nil, &bp)
		if bp.RequiredApprovals != 1 || !bp.DismissStaleApprovals || !bp.EnableMergeWhitelist {
			t.Fatalf("protection = %+v", bp)
		}
	})

	t.Run("actor worktrees next to main", func(t *testing.T) {
		out := a.mustCmd(demo, "actor", "setup", "claude", "agy")
		wt := filepath.Join(fam, "claude")
		if !strings.Contains(out, "created claude's worktree at") || !strings.Contains(out, "pushes blocked to origin") {
			t.Fatalf("actor setup:\n%s", out)
		}
		for k, want := range map[string]string{"user.name": "claude", "bottown.actor": "claude", "bottown.family": "anthropic", "bottown.instance": a.name} {
			if got := git(t, wt, "config", "--worktree", "--get", k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		// Nothing leaks into main's own config.
		if out, _ := exec.Command("git", "-C", demo, "config", "--get", "bottown.actor").Output(); len(out) > 0 {
			t.Errorf("main has bottown.actor = %s", out)
		}
		if got := git(t, demo, "config", "--get", "user.name"); got == "claude" {
			t.Errorf("main's user.name became claude")
		}
		// Pushes to origin are blocked; pushes to bottown get no owner key.
		push := func(remote string) (string, error) {
			c := exec.Command("git", "-C", wt, "push", remote, "HEAD:refs/heads/actor-push-test")
			c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			b, err := c.CombinedOutput()
			return string(b), err
		}
		if out, err := push("origin"); err == nil || !strings.Contains(out, "bottown-blocked") {
			t.Errorf("push to origin from actor worktree: %v\n%s", err, out)
		}
		// Outside a run the worktree's read-only key can fetch, not push.
		if out, err := exec.Command("git", "-C", wt, "fetch", "-q", "bottown-"+a.name).CombinedOutput(); err != nil {
			t.Errorf("fetch from actor worktree: %v\n%s", err, out)
		}
		if out, err := push("bottown-" + a.name); err == nil || !strings.Contains(out, "403") {
			t.Errorf("push to bottown from actor worktree without a run: %v\n%s", err, out)
		}
		// Re-running repairs, refuses non-actors, and ls lists both.
		if out := a.mustCmd(demo, "actor", "setup", "claude"); !strings.Contains(out, "repairing claude's worktree") {
			t.Errorf("second setup:\n%s", out)
		}
		if out, err := a.cmd(demo, "actor", "setup", "manager"); err == nil || !strings.Contains(out, "not an actor") {
			t.Errorf("setup manager: %v %s", err, out)
		}
		if out, err := a.cmd(wt, "actor", "setup", "kimi"); err == nil || !strings.Contains(out, "main checkout") {
			t.Errorf("setup from an actor worktree: %v %s", err, out)
		}
		ls := a.mustCmd(demo, "actor", "ls")
		if !strings.Contains(ls, "claude") || !strings.Contains(ls, "agy") || !strings.Contains(ls, "(detached)") {
			t.Errorf("actor ls:\n%s", ls)
		}
	})

	t.Run("clone bootstraps a fam", func(t *testing.T) {
		fam2 := filepath.Join(t.TempDir(), "famtest")
		out := a.mustCmd("", "clone", demo, fam2, "--actors", "claude,agy")
		main2 := filepath.Join(fam2, "main")
		for _, want := range []string{"origin https://example.invalid/demo.git", "protected bottown/famtest:main", "created claude's worktree", "created agy's worktree", "is ready"} {
			if !strings.Contains(out, want) {
				t.Errorf("clone output lacks %q:\n%s", want, out)
			}
		}
		if got := git(t, main2, "remote", "get-url", "origin"); got != "https://example.invalid/demo.git" {
			t.Errorf("main origin = %s (a local path must never be origin)", got)
		}
		if got := git(t, main2, "remote", "get-url", "bottown-"+a.name); !strings.HasSuffix(got, "/git/bottown/famtest.git") {
			t.Errorf("bottown remote = %s", got)
		}
		if got := git(t, filepath.Join(fam2, "claude"), "config", "--worktree", "--get", "bottown.actor"); got != "claude" {
			t.Errorf("claude worktree actor = %s", got)
		}
		a.mustAPI(owner, "GET", "/repos/bottown/famtest/branch_protections/main", nil, nil)
		// Re-running repo add in <fam>/main keeps the same repo (not "main").
		if out := a.mustCmd(main2, "repo", "add"); !strings.Contains(out, "famtest is hosted") || strings.Contains(out, "updated remote") {
			t.Errorf("repo add in fam main:\n%s", out)
		}
		if out, err := a.cmd(main2, "repo", "add", "other"); err == nil || !strings.Contains(out, "already points at") {
			t.Errorf("repo add with a conflicting name: %v %s", err, out)
		}
		if out, err := a.cmd("", "clone", demo, fam2); err == nil || !strings.Contains(out, "not empty") {
			t.Errorf("clone into an existing fam: %v %s", err, out)
		}
	})

	t.Run("a hosted checkout resolves its instance", func(t *testing.T) {
		if got := git(t, demo, "config", "--local", "--get", "bottown.instance"); got != a.name {
			t.Fatalf("bottown.instance = %q", got)
		}
		out, err := a.cmdResolved(demo, "status")
		if err != nil || !strings.Contains(out, "instance  "+a.name+" (from git config bottown.instance)") {
			t.Fatalf("status in checkout: %v\n%s", err, out)
		}
		// Without the config key, the remote alone identifies the instance.
		git(t, demo, "config", "--local", "--unset", "bottown.instance")
		out, err = a.cmdResolved(demo, "status")
		git(t, demo, "config", "--local", "bottown.instance", a.name)
		if err != nil || !strings.Contains(out, "(from git remote)") {
			t.Fatalf("status by remote: %v\n%s", err, out)
		}
	})

	t.Run("web login links", func(t *testing.T) {
		noFollow := func(jar http.CookieJar) *http.Client {
			return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		}
		jar, _ := cookiejar.New(nil)
		link := strings.TrimSpace(a.mustCmd(demo, "web", "forgejo", "--print"))
		resp, err := noFollow(jar).Get(link)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if want := a.cfg.ForgejoURL() + "/bottown/demo"; resp.StatusCode != 303 || resp.Header.Get("Location") != want {
			t.Fatalf("forgejo login: %d -> %s, want 303 -> %s", resp.StatusCode, resp.Header.Get("Location"), want)
		}
		// The cookie set by bottown's port is sent to Forgejo's port.
		resp, err = noFollow(jar).Get(a.cfg.ForgejoURL() + "/user/settings")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("forgejo settings with session: %d (not signed in)", resp.StatusCode)
		}
		if resp, err := http.Get(link); err != nil || resp.StatusCode != 401 {
			t.Fatalf("reused login link: %v %v", err, resp.StatusCode)
		}

		ui := strings.TrimSpace(a.mustCmd("", "web", "health", "--print"))
		resp, err = (&http.Client{Jar: jar}).Get(ui)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), "Signed in as owner") {
			t.Fatalf("web ui page: %d %s", resp.StatusCode, b)
		}
		if resp, _ := http.Get(a.cfg.ServerURL() + "/"); resp.StatusCode != 401 {
			t.Fatalf("web ui without session: %d", resp.StatusCode)
		}
	})

	// claude implements and codex verifies; roles are per run (step 2), so
	// here they are only how the test uses each actor.
	impl := a.mustCmd("", "credential", "key", "claude", "--full")
	impl = strings.TrimSpace(impl)
	ver := strings.TrimSpace(a.mustCmd("", "credential", "key", "codex", "--full"))

	t.Run("policy and protection bound the bots", func(t *testing.T) {
		head := git(t, demo, "rev-parse", "HEAD")
		if code, b := a.api(impl, "POST", "/repos/bottown/demo/statuses/"+head, map[string]string{"state": "success", "context": "bottown/verify"}); code != 403 {
			t.Errorf("implementer commit status: %d %s", code, b)
		}
		if code, _ := a.api(impl, "GET", "/repos/bottown/demo/issues", nil); code != 200 {
			t.Errorf("implementer read issues: %d", code)
		}
		if out, err := a.pushAs(demo, "claude", impl, "main:refs/heads/main"); err == nil && !strings.Contains(out, "Everything up-to-date") {
			t.Errorf("implementer push to protected main succeeded:\n%s", out)
		}
		var me struct{ Login string }
		req, _ := http.NewRequest("GET", a.cfg.ServerURL()+"/forgejo/api/v1/user", nil)
		req.Header.Set("Authorization", "Bearer "+impl)
		req.Header.Set("Sudo", "owner")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		json.NewDecoder(resp.Body).Decode(&me)
		resp.Body.Close()
		if me.Login != "claude" {
			t.Errorf("Sudo header honoured: acting as %q", me.Login)
		}
		if code, _ := a.api("btk_bogus", "GET", "/user", nil); code != 401 {
			t.Errorf("bogus key: %d", code)
		}
		if out, err := a.cmd("", "credential", "key", "manager"); err == nil {
			t.Errorf("manager got an API key: %s", out)
		}
	})

	t.Run("approvals: self, stale, merge gate", func(t *testing.T) {
		git(t, demo, "checkout", "-q", "-b", "feature")
		os.WriteFile(filepath.Join(demo, "a.txt"), []byte("1\n"), 0o644)
		git(t, demo, "add", ".")
		git(t, demo, "commit", "-q", "-m", "feature 1")
		if out, err := a.pushAs(demo, "claude", impl, "feature"); err != nil {
			t.Fatalf("implementer push feature: %v\n%s", err, out)
		}
		var pr struct {
			Number int `json:"number"`
			Head   struct{ Sha string }
		}
		a.mustAPI(impl, "POST", "/repos/bottown/demo/pulls", map[string]string{"head": "feature", "base": "main", "title": "feature"}, &pr)
		prPath := fmt.Sprintf("/repos/bottown/demo/pulls/%d", pr.Number)

		if code, b := a.api(impl, "POST", prPath+"/reviews", map[string]any{"event": "APPROVED", "body": "lgtm", "commit_id": pr.Head.Sha}); code < 400 {
			t.Errorf("implementer self-approval accepted: %d %s", code, b)
		}
		merge := map[string]string{"Do": "merge"}
		if code, b := a.api(owner, "POST", prPath+"/merge", merge); code < 400 {
			t.Fatalf("merge without approval accepted: %d %s", code, b)
		}
		a.mustAPI(ver, "POST", prPath+"/reviews", map[string]any{"event": "APPROVED", "body": "verified", "commit_id": pr.Head.Sha}, nil)

		// A new commit must make the approval stale.
		os.WriteFile(filepath.Join(demo, "a.txt"), []byte("2\n"), 0o644)
		git(t, demo, "commit", "-q", "-am", "feature 2")
		if out, err := a.pushAs(demo, "claude", impl, "feature"); err != nil {
			t.Fatalf("push feature 2: %v\n%s", err, out)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			var reviews []struct {
				State     string `json:"state"`
				Stale     bool   `json:"stale"`
				Dismissed bool   `json:"dismissed"`
			}
			a.mustAPI(owner, "GET", prPath+"/reviews", nil, &reviews)
			if len(reviews) > 0 && (reviews[len(reviews)-1].Stale || reviews[len(reviews)-1].Dismissed) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("approval not dismissed after new commit: %+v", reviews)
			}
			time.Sleep(time.Second)
		}
		if code, b := a.api(owner, "POST", prPath+"/merge", merge); code < 400 {
			t.Fatalf("merge with stale approval accepted: %d %s", code, b)
		}
		var cur struct{ Head struct{ Sha string } }
		a.mustAPI(owner, "GET", prPath, nil, &cur)
		a.mustAPI(ver, "POST", prPath+"/reviews", map[string]any{"event": "APPROVED", "body": "re-verified", "commit_id": cur.Head.Sha}, nil)
		if code, _ := a.api(impl, "POST", prPath+"/merge", merge); code != 403 {
			t.Errorf("implementer merge: %d, want policy 403", code)
		}
		// Mergeability is recomputed asynchronously; retry briefly (v1 saw transient 405s).
		deadline = time.Now().Add(30 * time.Second)
		for {
			code, b := a.api(owner, "POST", prPath+"/merge", merge)
			if code == 200 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("owner merge after approval: %d %s", code, b)
			}
			time.Sleep(time.Second)
		}
	})

	t.Run("run keys: role, repo scope, family rule, lifecycle", func(t *testing.T) {
		// A second anthropic actor, via the desired-state override file, so
		// the same-family rule has something to refuse.
		def, err := os.ReadFile(filepath.Join(src, "internal", "config", "default.toml"))
		if err != nil {
			t.Fatal(err)
		}
		override := string(def) + "\n[[actors]]\nname = \"claude2\"\nfamily = \"anthropic\"\nharness = \"claude\"\n"
		c := exec.Command("docker", "exec", "-i", "-u", "git", a.cfg.Container(), "sh", "-c", "cat > /data/bottown/bottown.toml")
		c.Stdin = strings.NewReader(override)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("write override: %v %s", err, out)
		}
		// `up` on a running instance reconciles now: no restart needed.
		if out := a.up(); !strings.Contains(out, "created account claude2") {
			t.Fatalf("override not applied:\n%s", out)
		}

		runs := func(key, method, path string, body any) (int, map[string]any) {
			t.Helper()
			var r io.Reader
			if body != nil {
				b, _ := json.Marshal(body)
				r = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(method, a.cfg.ServerURL()+path, r)
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var m map[string]any
			json.NewDecoder(resp.Body).Decode(&m)
			return resp.StatusCode, m
		}

		var issue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "run test"}, &issue)
		code, impl := runs(owner, "POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "claude", "repo": "demo", "issue": issue.Number})
		if code != 201 || impl["branch"] != fmt.Sprintf("issue/%d", issue.Number) {
			t.Fatalf("implement run: %d %v", code, impl)
		}
		ik := impl["key"].(string)
		branch := impl["branch"].(string)

		// Repo scope and owner-only endpoints.
		if code, _ := a.api(ik, "GET", "/repos/bottown/demo/issues/1", nil); code != 200 {
			t.Errorf("implement run read own repo: %d", code)
		}
		for _, sub := range []string{"dependencies", "blocks"} {
			p := fmt.Sprintf("/repos/bottown/demo/issues/%d/%s", issue.Number, sub)
			if code, b := a.api(ik, "GET", p, nil); code != 200 {
				t.Errorf("implement run GET %s: %d %s", sub, code, b)
			}
			for _, m := range []string{"POST", "DELETE"} {
				if code, _ := a.api(ik, m, p, map[string]any{"index": 1}); code != 403 {
					t.Errorf("implement run %s %s: %d, want 403", m, sub, code)
				}
			}
		}
		if code, _ := a.api(ik, "GET", "/repos/bottown/elsewhere", nil); code != 403 {
			t.Errorf("implement run read other repo: %d, want 403", code)
		}
		if code, _ := runs(ik, "POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "claude", "repo": "demo", "issue": issue.Number}); code != 409 {
			t.Errorf("run key creating runs: %d, want 409 (end the current run first)", code)
		}

		// The implementer pushes its branch and opens a PR, but cannot review.
		git(t, demo, "checkout", "-q", "-B", branch, "bottown-"+a.name+"/main")
		os.WriteFile(filepath.Join(demo, "run.txt"), []byte("run\n"), 0o644)
		git(t, demo, "add", ".")
		git(t, demo, "commit", "-q", "-m", "run change")
		if out, err := a.pushAs(demo, "claude", ik, branch); err != nil {
			t.Fatalf("implement run push: %v\n%s", err, out)
		}
		var pr struct {
			Number int64
			Head   struct{ Sha string }
		}
		a.mustAPI(ik, "POST", "/repos/bottown/demo/pulls", map[string]string{"head": branch, "base": "main", "title": "run PR"}, &pr)
		if code, _ := a.api(ik, "POST", fmt.Sprintf("/repos/bottown/demo/pulls/%d/reviews", pr.Number), map[string]string{"event": "COMMENT", "body": "x"}); code != 403 {
			t.Errorf("implement run review: %d, want 403", code)
		}

		// Verify runs: not by the author, not same-family without override.
		verify := func(actor string, allow bool) (int, map[string]any) {
			return runs(owner, "POST", "/v1/runs", map[string]any{"kind": "verify", "actor": actor, "repo": "demo", "pr": pr.Number, "allow_same_family": allow})
		}
		if code, m := verify("claude", false); code != 409 {
			t.Errorf("author verifying own PR: %d %v", code, m)
		}
		if code, m := verify("claude2", false); code != 409 || !strings.Contains(fmt.Sprint(m["error"]), "anthropic") {
			t.Errorf("same-family verify without override: %d %v", code, m)
		}
		code, sameFamily := verify("claude2", true)
		if code != 201 || sameFamily["allow_same_family"] != true {
			t.Errorf("same-family verify with override: %d %v", code, sameFamily)
		}
		if code, m := runs(owner, "DELETE", "/v1/runs/"+sameFamily["id"].(string), map[string]string{"status": "revoked"}); code != 200 {
			t.Fatalf("end same-family run: %d %v", code, m)
		}
		code, ver := verify("codex", false)
		if code != 201 || ver["sha"] != pr.Head.Sha {
			t.Fatalf("verify run: %d %v (head %s)", code, ver, pr.Head.Sha)
		}
		vk := ver["key"].(string)
		prPath := fmt.Sprintf("/repos/bottown/demo/pulls/%d", pr.Number)
		// Reviews only through review_submit, which pins the commit.
		if code, _ := a.api(vk, "POST", prPath+"/reviews", map[string]any{"event": "APPROVED", "body": "ok", "commit_id": pr.Head.Sha}); code != 403 {
			t.Errorf("verify run raw review: %d, want 403", code)
		}
		if code, _ := a.api(vk, "POST", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", pr.Number), map[string]string{"body": "note"}); code != 201 {
			t.Errorf("verify run comment: %d", code)
		}
		if code, _ := a.api(vk, "POST", "/repos/bottown/demo/pulls", map[string]string{"head": branch, "base": "main", "title": "x"}); code != 403 {
			t.Errorf("verify run create PR: %d, want 403", code)
		}
		if out, err := a.pushAs(demo, "codex", vk, branch+":refs/heads/codex-push"); err == nil {
			t.Errorf("verify run push succeeded:\n%s", out)
		}

		// Ending a run kills its key; short TTLs expire.
		for _, run := range []map[string]any{impl, ver} {
			if code, m := runs(owner, "DELETE", "/v1/runs/"+run["id"].(string), map[string]string{"status": "succeeded"}); code != 200 || m["status"] != "succeeded" {
				t.Fatalf("end run: %d %v", code, m)
			}
		}
		if code, _ := a.api(ik, "GET", "/repos/bottown/demo/issues/1", nil); code != 401 {
			t.Errorf("ended run key: %d, want 401", code)
		}
		code, short := runs(owner, "POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "codex", "repo": "demo", "issue": issue.Number, "ttl_seconds": 1})
		if code != 201 {
			t.Fatalf("short run: %d %v", code, short)
		}
		time.Sleep(1500 * time.Millisecond)
		if code, _ := a.api(short["key"].(string), "GET", "/user", nil); code != 401 {
			t.Errorf("expired run key: %d, want 401", code)
		}
	})

	t.Run("read-only actor keys and actor self-start", func(t *testing.T) {
		call := func(key, method, path string, body any) (int, map[string]any) {
			t.Helper()
			var r io.Reader
			if body != nil {
				b, _ := json.Marshal(body)
				r = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(method, a.cfg.ServerURL()+path, r)
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var m map[string]any
			json.NewDecoder(resp.Body).Decode(&m)
			return resp.StatusCode, m
		}
		// The read key actor setup stored in claude's worktree git dir.
		gitDir := git(t, filepath.Join(fam, "claude"), "rev-parse", "--absolute-git-dir")
		var stored struct{ Key string }
		b, err := os.ReadFile(filepath.Join(gitDir, "bottown", "actor.json"))
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(b, &stored)
		rk := stored.Key

		var issue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "self-start"}, &issue)
		if code, _ := a.api(rk, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d", issue.Number), nil); code != 200 {
			t.Errorf("read key GET issue: %d", code)
		}
		if code, _ := a.api(rk, "POST", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue.Number), map[string]string{"body": "x"}); code != 403 {
			t.Errorf("read key POST comment via proxy: %d, want 403", code)
		}
		if code, _ := call(rk, "POST", "/v1/forge/comment", map[string]any{"repo": "demo", "number": issue.Number, "body": "x"}); code != 403 {
			t.Errorf("read key POST /v1/forge/comment: %d, want 403", code)
		}
		// Filing an issue is the one write a read key may make.
		if code, got := call(rk, "POST", "/v1/forge/issue", map[string]any{"repo": "demo", "title": "from a read key"}); code != 200 || got["repo"] != "demo" {
			t.Errorf("read key POST /v1/forge/issue: %d %v", code, got)
		}
		if code, _ := call(rk, "POST", "/v1/forge/issue", map[string]any{"title": "no repo"}); code != 400 {
			t.Errorf("read key POST /v1/forge/issue without repo: %d, want 400", code)
		}

		// Self-start: only for itself, never with a same-family override.
		if code, _ := call(rk, "POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "codex", "repo": "demo", "issue": issue.Number}); code != 403 {
			t.Errorf("starting a run for another actor: %d", code)
		}
		code, run := call(rk, "POST", "/v1/runs", map[string]any{"kind": "implement", "repo": "demo", "issue": issue.Number})
		if code != 201 || run["actor"] != "claude" || run["started_by"] != "claude" {
			t.Fatalf("self-start: %d %v", code, run)
		}
		runKey, runID := run["key"].(string), run["id"].(string)
		if code, _ := call(runKey, "POST", "/v1/runs", map[string]any{"kind": "implement", "repo": "demo", "issue": issue.Number}); code != 409 {
			t.Errorf("run key starting another run: %d, want 409", code)
		}
		code, got := call(runKey, "GET", "/v1/forge/issue", nil)
		if code != 200 || got["title"] != "self-start" {
			t.Errorf("GET /v1/forge/issue with run key: %d %v", code, got)
		}
		if code, got := call(runKey, "POST", "/v1/forge/comment", map[string]any{"body": "working on it"}); code != 200 || got["author"] != "claude" {
			t.Errorf("POST /v1/forge/comment with run key: %d %v", code, got)
		}
		if code, _ := call(runKey, "POST", "/v1/forge/review", map[string]any{"event": "APPROVED", "body": "x", "commit_id": "x"}); code != 403 {
			t.Errorf("review from an implement run: %d, want 403", code)
		}
		// Ending: another actor cannot; the actor itself can.
		other := strings.TrimSpace(a.mustCmd("", "credential", "key", "agy"))
		if code, _ := call(other, "DELETE", "/v1/runs/"+runID, map[string]string{"status": "revoked"}); code != 404 {
			t.Errorf("another actor ending the run: %d, want 404", code)
		}
		if code, m := call(rk, "DELETE", "/v1/runs/"+runID, map[string]string{"status": "succeeded"}); code != 200 || m["status"] != "succeeded" {
			t.Errorf("actor ending its own run: %d %v", code, m)
		}
		if code, _ := call(runKey, "GET", "/v1/forge/issue", nil); code != 401 {
			t.Errorf("ended run key: %d, want 401", code)
		}
	})

	t.Run("agent CLI in actor worktrees", func(t *testing.T) {
		cw, aw := filepath.Join(fam, "claude"), filepath.Join(fam, "agy")
		// Agent commands find their actor from the worktree, not $BOTTOWN_INSTANCE.
		agent := func(dir string, stdin string, args ...string) (string, error) {
			c := exec.Command(bin, args...)
			for _, e := range a.env() {
				if !strings.HasPrefix(e, "BOTTOWN_INSTANCE=") {
					c.Env = append(c.Env, e)
				}
			}
			c.Dir, c.Stdin = dir, strings.NewReader(stdin)
			b, err := c.CombinedOutput()
			return string(b), err
		}
		must := func(dir, stdin string, args ...string) string {
			t.Helper()
			out, err := agent(dir, stdin, args...)
			if err != nil {
				t.Fatalf("bottown %s in %s: %v\n%s", strings.Join(args, " "), filepath.Base(dir), err, out)
			}
			return out
		}
		if out, err := agent(demo, "", "whoami"); err == nil || !strings.Contains(out, "not an actor worktree") {
			t.Errorf("agent CLI in main: %v %s", err, out)
		}
		if out := must(cw, "", "whoami"); !strings.Contains(out, "actor claude") || !strings.Contains(out, "no active run") ||
			strings.Contains(out, "last run") || !strings.Contains(out, "(detached), ") || !strings.Contains(out, " bottown-"+a.name+"/main") {
			t.Errorf("idle whoami: %s", out)
		}

		// Filing issues works outside a run, in the worktree's repository.
		out := must(cw, "found while talking", "issue", "create", "--title", "filed from chat", "--body-file", "-")
		var filed int64
		if _, err := fmt.Sscanf(out, "filed #%d in demo", &filed); err != nil {
			t.Fatalf("issue create outside a run: %s", out)
		}
		var got struct {
			Body string
			User struct{ Login string }
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d", filed), nil, &got)
		if got.User.Login != "claude" || !strings.Contains(got.Body, "found while talking") || !strings.Contains(got.Body, "filed by claude, anthropic outside a run") {
			t.Errorf("filed issue: %+v", got)
		}

		var issue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "agent cli", "body": "add agent.txt"}, &issue)
		n := fmt.Sprint(issue.Number)

		os.WriteFile(filepath.Join(cw, "stray.txt"), []byte("x"), 0o644)
		if out, err := agent(cw, "", "run", "start", "implement", n); err == nil || !strings.Contains(out, "uncommitted") {
			t.Errorf("run start in a dirty worktree: %v %s", err, out)
		}
		os.Remove(filepath.Join(cw, "stray.txt"))

		// As a slash command passes it: /bottown-implement issue #N.
		prompt := must(cw, "", "run", "start", "implement", "issue", "#"+n)
		if !strings.Contains(prompt, "issue show") || !strings.Contains(prompt, "run end --summary-file -") || !strings.Contains(prompt, "add agent.txt") {
			t.Fatalf("implement prompt:\n%s", prompt)
		}
		if got := git(t, cw, "rev-parse", "--abbrev-ref", "HEAD"); got != "issue/"+n {
			t.Errorf("worktree branch %s", got)
		}
		if out := must(cw, "", "whoami"); !strings.Contains(out, "implement issue #"+n) {
			t.Errorf("whoami in run: %s", out)
		}
		if out, err := agent(cw, "", "run", "start", "implement", n); err == nil || !strings.Contains(out, "already in run") {
			t.Errorf("second run start: %v %s", err, out)
		}
		if out := must(cw, "", "issue", "show"); !strings.Contains(out, "agent cli") {
			t.Errorf("issue show: %s", out)
		}
		if out, err := agent(cw, "", "issue", "create", "--repo", "other", "--title", "x"); err == nil || !strings.Contains(out, "confined to") {
			t.Errorf("issue create in another repo from a run: %v %s", err, out)
		}
		if out := must(cw, "", "issue", "create", "--title", "follow-up from a run"); !strings.Contains(out, "in demo") {
			t.Errorf("issue create in a run: %s", out)
		}
		if out := must(cw, "", "issue", "list"); !strings.Contains(out, "agent cli") {
			t.Errorf("issue list: %s", out)
		}
		if out := must(cw, "", "issue", "list", "--json"); !strings.Contains(out, `"title": "agent cli"`) {
			t.Errorf("issue list --json: %s", out)
		}

		os.WriteFile(filepath.Join(cw, "agent.txt"), []byte("agent\n"), 0o644)
		git(t, cw, "add", "agent.txt")
		git(t, cw, "commit", "-q", "-m", "agent change")
		if out, err := exec.Command("git", "-C", cw, "push", "-q", "bottown-"+a.name, "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("push from actor worktree in a run: %v %s", err, out)
		}
		out = must(cw, "what changed: agent.txt", "pr", "create", "--title", "agent cli PR", "--body-file", "-")
		var prn int64
		if _, err := fmt.Sscanf(out, "opened !%d", &prn); err != nil {
			t.Fatalf("pr create: %s", out)
		}
		if out := must(cw, "", "pr", "list"); !strings.Contains(out, "agent cli PR") || !strings.Contains(out, "needs review") {
			t.Errorf("pr list: %s", out)
		}
		if out := must(cw, "", "pr", "list", "--json"); !strings.Contains(out, `"review_state": "needs review"`) {
			t.Errorf("pr list --json: %s", out)
		}
		if out, err := agent(cw, "", "review", "submit", "--event", "APPROVED", "--commit", "x", "--body", "x"); err == nil || !strings.Contains(out, "verify run") {
			t.Errorf("review from implement run: %v %s", err, out)
		}
		if out := must(cw, "implemented agent.txt", "run", "end", "--summary-file", "-"); !strings.Contains(out, "succeeded; key revoked") {
			t.Errorf("run end: %s", out)
		}
		if out := must(cw, "", "whoami"); !strings.Contains(out, "no active run") ||
			!strings.Contains(out, "implement issue #"+n+" in bottown/demo, succeeded") ||
			!strings.Contains(out, "on issue/"+n+", 1 commit ahead of bottown-"+a.name+"/main") {
			t.Errorf("whoami after end: %s", out)
		}
		if out := must(cw, "", "issue", "list"); !strings.Contains(out, "follow-up from a run") {
			t.Errorf("issue list outside run: %s", out)
		}
		if out := must(cw, "", "issue", "show", n); !strings.Contains(out, "agent cli") {
			t.Errorf("issue show outside run: %s", out)
		}
		if out := must(cw, "", "pr", "show", fmt.Sprint(prn)); !strings.Contains(out, "agent cli PR") {
			t.Errorf("pr show outside run: %s", out)
		}
		if b, err := exec.Command("git", "-C", cw, "push", "bottown-"+a.name, "HEAD:refs/heads/after-end").CombinedOutput(); err == nil {
			t.Errorf("push after run end succeeded: %s", b)
		}

		// agy verifies from its own worktree.
		vp := must(aw, "", "run", "start", "verify", fmt.Sprint(prn))
		sha := git(t, aw, "rev-parse", "HEAD")
		if !strings.Contains(vp, "review submit --commit "+sha) {
			t.Fatalf("verify prompt lacks the pinned commit %s:\n%s", sha, vp)
		}
		if out := must(aw, "", "pr", "show", "--diff"); !strings.Contains(out, "agent.txt") || !strings.Contains(out, "by claude") ||
			!strings.Contains(out, " agent.txt | 1 +\n 1 file changed, 1 insertion(+), 0 deletions(-)") || !strings.Contains(out, "+agent") {
			t.Errorf("pr show --diff: %s", out)
		}
		if out := must(aw, "", "pr", "show", "--stat"); !strings.Contains(out, " agent.txt | 1 +") || strings.Contains(out, "+agent") {
			t.Errorf("pr show --stat: %s", out)
		}
		if out := must(aw, "", "pr", "list"); !strings.Contains(out, "needs review") {
			t.Errorf("pr list in verify run before review: %s", out)
		}
		if out := must(aw, "looks right", "review", "submit", "--event", "APPROVED", "--commit", sha, "--body-file", "-"); !strings.Contains(out, "APPROVED by agy") || !strings.Contains(out, "official true") {
			t.Errorf("review submit: %s", out)
		}
		if out := must(aw, "", "pr", "list"); !strings.Contains(out, "approved") {
			t.Errorf("pr list in verify run after review: %s", out)
		}
		if out, err := agent(aw, "", "api", "POST", fmt.Sprintf("/repos/bottown/demo/pulls/%d/merge", prn), "--data", `{"Do":"merge"}`); err == nil {
			t.Errorf("api merge from verify run succeeded: %s", out)
		}
		must(aw, "", "run", "end", "--summary", "approved")

		var comments []struct {
			User struct{ Login string }
			Body string
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue.Number), nil, &comments)
		if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "implemented agent.txt") {
			t.Errorf("implement handover on the issue: %+v", comments)
		}

		// A discuss run reads and comments on the issue, and changes nothing.
		if out, err := agent(cw, "x", "comment", n, "--body-file", "-"); err == nil || !strings.Contains(out, "read-only") {
			t.Errorf("comment outside a run: %v %s", err, out)
		}
		dp := must(cw, "", "run", "start", "discuss", "#"+n)
		if !strings.Contains(dp, "**discussant**") || !strings.Contains(dp, "comment --body-file -") || !strings.Contains(dp, "add agent.txt") {
			t.Fatalf("discuss prompt:\n%s", dp)
		}
		if head, main := git(t, cw, "rev-parse", "HEAD"), git(t, cw, "rev-parse", "bottown-"+a.name+"/main"); head != main {
			t.Errorf("discuss worktree at %s, want the default branch head %s", head, main)
		}
		if out := must(cw, "", "whoami"); !strings.Contains(out, "discuss issue #"+n) {
			t.Errorf("whoami in discuss run: %s", out)
		}
		if out := must(cw, "blah, not foo", "comment", "--body-file", "-"); !strings.Contains(out, "commented") {
			t.Errorf("comment in discuss run: %s", out)
		}
		if out, err := agent(cw, "", "pr", "create", "--title", "x"); err == nil || !strings.Contains(out, "implement run") {
			t.Errorf("pr create in discuss run: %v %s", err, out)
		}
		if out, err := agent(cw, "", "review", "submit", "--event", "COMMENT", "--commit", "x", "--body", "x"); err == nil || !strings.Contains(out, "verify run") {
			t.Errorf("review in discuss run: %v %s", err, out)
		}
		if out, err := agent(cw, "", "api", "PATCH", "/repos/bottown/demo/issues/"+n, "--data", `{"title":"renamed"}`); err == nil {
			t.Errorf("issue edit in discuss run succeeded: %s", out)
		}
		if b, err := exec.Command("git", "-C", cw, "push", "bottown-"+a.name, "HEAD:refs/heads/discuss-push").CombinedOutput(); err == nil {
			t.Errorf("push in discuss run succeeded: %s", b)
		}
		if out := must(cw, "", "run", "end", "--summary", "answered blah"); !strings.Contains(out, "succeeded; key revoked") {
			t.Errorf("discuss run end: %s", out)
		}
		comments = nil
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue.Number), nil, &comments)
		if k := len(comments); k < 2 || comments[k-2].Body != "blah, not foo" || !strings.Contains(comments[k-1].Body, "· discuss · claude") ||
			!strings.Contains(comments[k-1].Body, "Discussed against") || !strings.Contains(comments[k-1].Body, "answered blah") {
			t.Errorf("discuss comments on the issue: %+v", comments)
		}
	})

	t.Run("mcp whoami", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cl := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   a.cfg.ServerURL() + "/mcp",
			HTTPClient: &http.Client{Transport: bearer{impl}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), `"actor":"claude"`) || !strings.Contains(string(b), `"family":"anthropic"`) {
			t.Fatalf("whoami = %s", b)
		}
		req, _ := http.NewRequest("POST", a.cfg.ServerURL()+"/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer btk_bogus")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("bogus MCP key: %d", resp.StatusCode)
		}
	})

	t.Run("mcp forge tools in runs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		connect := func(endpoint, key string) *mcp.ClientSession {
			t.Helper()
			hc := http.DefaultClient
			if key != "" {
				hc = &http.Client{Transport: bearer{key}}
			}
			cl := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
			sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sess.Close() })
			return sess
		}
		callRaw := func(sess *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
			t.Helper()
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if res.IsError {
				var msg string
				for _, c := range res.Content {
					if tc, ok := c.(*mcp.TextContent); ok {
						msg += tc.Text
					}
				}
				return nil, msg
			}
			return res, ""
		}
		call := func(sess *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
			t.Helper()
			res, msg := callRaw(sess, name, args)
			if msg != "" {
				return nil, msg
			}
			b, _ := json.Marshal(res.StructuredContent)
			var m map[string]any
			json.Unmarshal(b, &m)
			return m, ""
		}
		newRun := func(body map[string]any) map[string]any {
			t.Helper()
			b, _ := json.Marshal(body)
			req, _ := http.NewRequest("POST", a.cfg.ServerURL()+"/v1/runs", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+owner)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var m map[string]any
			json.NewDecoder(resp.Body).Decode(&m)
			if resp.StatusCode != 201 {
				t.Fatalf("create run: %d %v", resp.StatusCode, m)
			}
			return m
		}
		endRun := func(id string) {
			t.Helper()
			req, _ := http.NewRequest("DELETE", a.cfg.ServerURL()+"/v1/runs/"+id, strings.NewReader(`{"status":"revoked"}`))
			req.Header.Set("Authorization", "Bearer "+owner)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("end run %s: %d %s", id, resp.StatusCode, b)
			}
		}

		var issue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "mcp test", "body": "do the thing"}, &issue)
		impl := newRun(map[string]any{"kind": "implement", "actor": "claude", "repo": "demo", "issue": issue.Number})
		ik, branch := impl["key"].(string), impl["branch"].(string)
		// The key-in-URL fallback, for harnesses that cannot send headers.
		is := connect(a.cfg.ServerURL()+"/mcp/k/"+ik, "")

		who, _ := call(is, "whoami", nil)
		if run, _ := who["run"].(map[string]any); who["actor"] != "claude" || run["kind"] != "implement" {
			t.Fatalf("whoami in implement run = %v", who)
		}
		got, errMsg := call(is, "issue_get", nil)
		if errMsg != "" || got["title"] != "mcp test" {
			t.Fatalf("issue_get default: %v %s", got, errMsg)
		}
		res, errMsg := callRaw(is, "issue_list", nil)
		b, _ := json.Marshal(res.StructuredContent)
		if errMsg != "" || !strings.Contains(string(b), "mcp test") {
			t.Fatalf("issue_list: %s %s", string(b), errMsg)
		}
		if _, errMsg := call(is, "issue_get", map[string]any{"repo": "elsewhere"}); !strings.Contains(errMsg, "confined") {
			t.Errorf("issue_get other repo: %q", errMsg)
		}
		if out, _ := call(is, "forge_api", map[string]any{"method": "GET", "path": "/repos/bottown/elsewhere"}); out["status"] != float64(403) {
			t.Errorf("forge_api other repo: %v", out)
		}

		git(t, demo, "checkout", "-q", "-B", branch, "bottown-"+a.name+"/main")
		os.WriteFile(filepath.Join(demo, "mcp.txt"), []byte("1\n"), 0o644)
		git(t, demo, "add", ".")
		git(t, demo, "commit", "-q", "-m", "mcp change")
		if out, err := a.pushAs(demo, "claude", ik, branch); err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
		pr, errMsg := call(is, "pr_create", map[string]any{"title": "mcp PR", "body": "done"})
		if errMsg != "" {
			t.Fatalf("pr_create: %s", errMsg)
		}
		res, errMsg = callRaw(is, "pr_list", nil)
		b, _ = json.Marshal(res.StructuredContent)
		if errMsg != "" || !strings.Contains(string(b), "mcp PR") {
			t.Fatalf("pr_list: %s %s", string(b), errMsg)
		}
		prn := int64(pr["number"].(float64))
		if _, errMsg := call(is, "review_submit", map[string]any{"event": "APPROVED", "body": "x", "commit_id": "abc"}); !strings.Contains(errMsg, "verify run") {
			t.Errorf("review_submit in implement run: %q", errMsg)
		}
		if out, _ := call(is, "forge_api", map[string]any{"method": "POST", "path": fmt.Sprintf("/repos/bottown/demo/pulls/%d/merge", prn), "body": map[string]string{"Do": "merge"}}); out["status"] != float64(403) {
			t.Errorf("forge_api merge: %v", out)
		}

		ver := newRun(map[string]any{"kind": "verify", "actor": "codex", "repo": "demo", "pr": prn})
		vs := connect(a.cfg.ServerURL()+"/mcp", ver["key"].(string))
		got, errMsg = call(vs, "pr_get", nil)
		if errMsg != "" || got["head_sha"] != ver["sha"] || !strings.Contains(fmt.Sprint(got["diff"]), "mcp.txt") {
			t.Fatalf("pr_get: %s head=%v run=%v", errMsg, got["head_sha"], ver["sha"])
		}
		if _, errMsg := call(vs, "pr_create", map[string]any{"title": "x"}); !strings.Contains(errMsg, "implement run") {
			t.Errorf("pr_create in verify run: %q", errMsg)
		}
		if _, errMsg := call(vs, "review_submit", map[string]any{"event": "APPROVED", "body": "x", "commit_id": "deadbeef"}); !strings.Contains(errMsg, "not the commit") {
			t.Errorf("review at wrong commit: %q", errMsg)
		}
		// The PR moves after the verify run started: its verdict no longer covers it.
		os.WriteFile(filepath.Join(demo, "mcp.txt"), []byte("2\n"), 0o644)
		git(t, demo, "commit", "-q", "-am", "mcp change 2")
		if out, err := a.pushAs(demo, "claude", ik, branch); err != nil {
			t.Fatalf("push 2: %v\n%s", err, out)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			_, errMsg = call(vs, "review_submit", map[string]any{"event": "APPROVED", "body": "x", "commit_id": ver["sha"]})
			if strings.Contains(errMsg, "moved") || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond) // the PR head updates asynchronously
		}
		if !strings.Contains(errMsg, "moved") {
			t.Fatalf("review after PR moved: %q", errMsg)
		}
		endRun(ver["id"].(string))
		ver2 := newRun(map[string]any{"kind": "verify", "actor": "codex", "repo": "demo", "pr": prn})
		vs2 := connect(a.cfg.ServerURL()+"/mcp", ver2["key"].(string))
		rv, errMsg := call(vs2, "review_submit", map[string]any{"event": "APPROVED", "body": "verified", "commit_id": ver2["sha"]})
		if errMsg != "" || rv["commit_id"] != ver2["sha"] || !strings.Contains(fmt.Sprint(rv["body"]), "bottown verify run "+ver2["id"].(string)) {
			t.Fatalf("review_submit: %v %s", rv, errMsg)
		}
		if c, errMsg := call(vs2, "comment", map[string]any{"body": "handover"}); errMsg != "" || c["author"] != "codex" {
			t.Fatalf("comment: %v %s", c, errMsg)
		}
		for _, run := range []map[string]any{ver2, impl} {
			endRun(run["id"].(string))
		}
	})

	t.Run("run commands with a scripted harness", func(t *testing.T) {
		withHarness := func(script string, args ...string) (string, error) {
			c := exec.Command(bin, args...)
			c.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+script)
			c.Dir = demo
			out, err := c.CombinedOutput()
			return string(out), err
		}
		git(t, demo, "checkout", "-q", "main")
		var issue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "scripted run", "body": "add cli.txt"}, &issue)

		implement := "set -e; test -n \"$BOTTOWN_RUN_KEY\"; grep -q 'scripted run' \"$BOTTOWN_PROMPT_FILE\"; " +
			"echo cli > cli.txt; git add cli.txt; git -c commit.gpgsign=false commit -qm 'cli change'; git push -q origin HEAD; " +
			bin + ` mcp call pr_create '{"title":"cli PR","body":"scripted"}' > /dev/null; ` +
			// The script harness reads usage from claude-style result events.
			`echo '{"type":"result","num_turns":3,"total_cost_usd":0.25,"usage":{"input_tokens":100,"output_tokens":7}}'; ` +
			`echo 'implemented by script'`
		out, err := withHarness(implement, "run", "implement", "-a", "claude", fmt.Sprint(issue.Number))
		if err != nil || !strings.Contains(out, "succeeded (worktree removed)") {
			t.Fatalf("run implement: %v\n%s", err, out)
		}
		m := regexp.MustCompile(`run (r-[0-9a-f]+): implement`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no run id in: %s", out)
		}
		implRun := m[1]
		if _, err := os.Stat(filepath.Join(demo, ".bottown", "mirror.git")); err != nil {
			t.Fatalf("no mirror: %v", err)
		}
		if st := git(t, demo, "status", "--porcelain"); strings.Contains(st, ".bottown") {
			t.Fatalf(".bottown is not excluded: %s", st)
		}
		var prs []struct {
			Number int64
			Title  string
			User   struct{ Login string }
			Head   struct{ Sha string }
		}
		a.mustAPI(owner, "GET", "/repos/bottown/demo/pulls?state=open&limit=50", nil, &prs)
		var pr int64
		for _, p := range prs {
			if p.Title == "cli PR" && p.User.Login == "claude" {
				pr = p.Number
			}
		}
		if pr == 0 {
			t.Fatalf("no cli PR by claude: %+v", prs)
		}
		var comments []struct {
			User struct{ Login string }
			Body string
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue.Number), nil, &comments)
		if len(comments) == 0 || comments[len(comments)-1].User.Login != "claude" || !strings.Contains(comments[len(comments)-1].Body, "implemented by script") {
			t.Fatalf("handover comment: %+v", comments)
		}

		verify := "set -e; test \"$(cat cli.txt)\" = cli; " + bin +
			` mcp call review_submit "{\"event\":\"APPROVED\",\"body\":\"ok\",\"commit_id\":\"$(git rev-parse HEAD)\"}" > /dev/null; echo approved`
		if out, err := withHarness(verify, "run", "verify", "-a", "agy", fmt.Sprint(pr)); err != nil || !strings.Contains(out, "succeeded") {
			t.Fatalf("run verify: %v\n%s", err, out)
		}
		// A failing run keeps its worktree; -I is recorded even without a TTY.
		out, err = withHarness("echo boom; exit 3", "run", "implement", "-a", "kimi", "-I", fmt.Sprint(issue.Number))
		if err == nil || !strings.Contains(out, "failed (exit 3); worktree kept") {
			t.Fatalf("failing run: %v\n%s", err, out)
		}
		out = a.mustCmd(demo, "run", "ls")
		for _, want := range []string{"implement  #", "verify     !", "agy", "interactive  failed", "succeeded"} {
			if !strings.Contains(out, want) {
				t.Errorf("run ls lacks %q:\n%s", want, out)
			}
		}
		// endedInDemo counts the server's ended runs of demo in `run ls --all`
		// (RUN REPO KIND TARGET ACTOR FAMILY KEY ...), and whether kimi's is one.
		endedInDemo := func() (n int, kimi bool) {
			for _, l := range strings.Split(a.mustCmd(demo, "run", "ls", "--all"), "\n") {
				if f := strings.Fields(l); len(f) > 6 && f[1] == "demo" && f[6] == "ended" {
					n++
					kimi = kimi || f[4] == "kimi"
				}
			}
			return n, kimi
		}
		// ownerRuns calls /v1/runs with the owner key.
		ownerRuns := func(method, path string, body any, out any) {
			t.Helper()
			var r io.Reader
			if body != nil {
				b, _ := json.Marshal(body)
				r = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(method, a.cfg.ServerURL()+path, r)
			req.Header.Set("Authorization", "Bearer "+owner)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				t.Fatalf("%s %s: %s", method, path, resp.Status)
			}
			if out != nil {
				json.NewDecoder(resp.Body).Decode(out)
			}
		}
		// A run whose key expired while live cannot end itself: gc revokes it.
		var expired struct{ ID string }
		ownerRuns("POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "codex", "repo": "demo", "issue": issue.Number, "ttl_seconds": 1}, &expired)
		time.Sleep(1500 * time.Millisecond)
		// Everything here ended moments ago: --older-than purges nothing.
		if out := a.mustCmd(demo, "run", "gc", "--older-than", "1h"); !strings.Contains(out, "removed 2 runs, purged 0 ended runs") || !strings.Contains(out, "whose key expired while live") {
			t.Errorf("gc --older-than: %s", out)
		}
		var revoked struct {
			Status string
			Ended  *time.Time
		}
		ownerRuns("GET", "/v1/runs/"+expired.ID, nil, &revoked)
		if revoked.Status != "revoked" || revoked.Ended == nil {
			t.Errorf("expired run after gc: %+v, want ended revoked", revoked)
		}
		if n, _ := endedInDemo(); n < 2 {
			t.Errorf("ended demo runs before purge: %d", n)
		}
		// gc purges the ended server records, actor-worktree runs included,
		// but keeps kimi's: its failed run's files are kept for inspection.
		if out := a.mustCmd(demo, "run", "gc"); !strings.Contains(out, "removed 0 runs, purged ") || strings.Contains(out, "purged 0 ") {
			t.Errorf("gc: %s", out)
		}
		if n, kimi := endedInDemo(); n != 1 || !kimi {
			t.Errorf("after gc: %d ended demo runs (kimi %v), want only kimi's", n, kimi)
		}
		// Their digests survive: the launcher's with the harness's usage, and
		// the actor-worktree run's with its handover.
		if out := a.mustCmd(demo, "run", "show", implRun); !strings.Contains(out, "ended succeeded, purged (digest kept)") ||
			!strings.Contains(out, "usage 100 in, 7 out tokens · $0.2500 · 3 turns") || !strings.Contains(out, "summary implemented by script") {
			t.Errorf("run show of a purged run:\n%s", out)
		}
		var history []struct {
			ID, Actor, Summary string
			Purged             bool
		}
		hreq, _ := http.NewRequest("GET", a.cfg.ServerURL()+"/v1/runs?repo=demo&state=ended&history=1", nil)
		hreq.Header.Set("Authorization", "Bearer "+owner)
		if resp, err := http.DefaultClient.Do(hreq); err != nil {
			t.Fatal(err)
		} else {
			json.NewDecoder(resp.Body).Decode(&history)
			resp.Body.Close()
		}
		var agentDigest bool
		for _, h := range history {
			agentDigest = agentDigest || (h.Purged && h.Actor == "claude" && h.Summary == "implemented agent.txt")
		}
		if !agentDigest {
			t.Errorf("no digest of the actor-worktree run in the history: %+v", history)
		}
		if out := a.mustCmd(demo, "run", "gc", "--failed"); !strings.Contains(out, "removed 1 runs, purged 1 ended runs") {
			t.Errorf("gc --failed: %s", out)
		}
		if n, _ := endedInDemo(); n != 0 {
			t.Errorf("after gc --failed: %d ended demo runs, want 0", n)
		}
		if out := a.mustCmd(demo, "run", "ls"); strings.Contains(out, "worktree kept") || strings.Contains(out, "kimi") {
			t.Errorf("run ls after gc:\n%s", out)
		}

		var stopIssue struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": "stop run"}, &stopIssue)
		// The harness spawns a child, as harnesses spawn MCP servers, and
		// exits cleanly on TERM: stopping the run must reach the child too.
		// startLong starts a batch run of script, which writes its run id to
		// the file named by $ID_FILE, and returns the launcher, its output
		// and the run id.
		startLong := func(script string) (*exec.Cmd, *bytes.Buffer, string) {
			t.Helper()
			idFile := filepath.Join(t.TempDir(), "run-id")
			c := exec.Command(bin, "run", "implement", "-a", "claude", fmt.Sprint(stopIssue.Number))
			c.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+script, "ID_FILE="+idFile)
			c.Dir = demo
			var out bytes.Buffer
			c.Stdout, c.Stderr = &out, &out
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if c.Process != nil {
					_ = c.Process.Kill()
					_ = c.Wait()
				}
			})
			var id string
			deadline := time.Now().Add(10 * time.Second)
			for id == "" {
				b, _ := os.ReadFile(idFile)
				id = strings.TrimSpace(string(b))
				if id == "" {
					if time.Now().After(deadline) {
						t.Fatalf("long run did not write its id\n%s", out.String())
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			return c, &out, id
		}
		childFile := filepath.Join(t.TempDir(), "child-pid")
		longRun := `sleep 300 & trap 'exit 0' TERM; echo $! > ` + childFile + `; echo "$BOTTOWN_RUN_ID" > "$ID_FILE"; while :; do sleep 1; done`
		stopCmd, runOut, stopID := startLong(longRun)
		out, err = withHarness("exit 99", "run", "implement", "-a", "claude", fmt.Sprint(stopIssue.Number))
		if err == nil || !strings.Contains(out, "actor claude") || !strings.Contains(out, stopID) {
			t.Fatalf("second batch run at limit: %v\n%s", err, out)
		}
		if out, err := withHarness("echo interactive", "run", "discuss", "-a", "claude", "-I", fmt.Sprint(stopIssue.Number)); err != nil || !strings.Contains(out, "succeeded") {
			t.Fatalf("interactive run at batch limit: %v\n%s", err, out)
		}
		if out := a.mustCmd(demo, "run", "stop", stopID); !strings.Contains(out, "stopped") {
			t.Errorf("run stop: %s", out)
		}
		if err := stopCmd.Wait(); err == nil || !strings.Contains(runOut.String(), "failed") {
			t.Errorf("stopped launcher: %v\n%s", err, runOut.String())
		}
		b, _ := os.ReadFile(childFile)
		child, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatalf("harness child pid %q: %v", b, err)
		}
		for deadline := time.Now().Add(5 * time.Second); syscall.Kill(child, 0) != syscall.ESRCH; time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(child, syscall.SIGKILL)
				t.Errorf("harness child %d survived run stop", child)
				break
			}
		}
		if out := a.mustCmd(demo, "run", "show", stopID); !strings.Contains(out, "failed") {
			t.Errorf("stopped run record: %s", out)
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", stopIssue.Number), nil, &comments)
		if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "Stopped by the owner.") || !strings.Contains(comments[len(comments)-1].Body, "**failed**") {
			t.Errorf("stopped handover: %+v", comments)
		}
		if out := a.mustCmd(demo, "run", "gc", "--failed"); !strings.Contains(out, "removed 2 runs, purged 2 ended runs") {
			t.Errorf("gc stopped run: %s", out)
		}

		// A harness that ignores TERM, children included, is killed after
		// the grace period, and the run still ends as a stopped failure.
		stubborn, stubbornOut, stubbornID := startLong(`trap '' TERM; echo "$BOTTOWN_RUN_ID" > "$ID_FILE"; while :; do sleep 1; done`)
		if out, err := a.cmd(demo, "run", "stop", "--grace", "1s", stubbornID); err != nil || !strings.Contains(out, "killing its harness") || !strings.Contains(out, "stopped") {
			t.Errorf("run stop of a TERM-ignoring harness: %v\n%s", err, out)
		}
		if err := stubborn.Wait(); err == nil || !strings.Contains(stubbornOut.String(), "failed") {
			t.Errorf("killed launcher: %v\n%s", err, stubbornOut.String())
		}
		var killedRun struct{ Status string }
		kreq, _ := http.NewRequest("GET", a.cfg.ServerURL()+"/v1/runs/"+stubbornID, nil)
		kreq.Header.Set("Authorization", "Bearer "+owner)
		if resp, err := http.DefaultClient.Do(kreq); err != nil {
			t.Fatal(err)
		} else {
			json.NewDecoder(resp.Body).Decode(&killedRun)
			resp.Body.Close()
		}
		if killedRun.Status != "failed" {
			t.Errorf("killed run on the server: %+v", killedRun)
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", stopIssue.Number), nil, &comments)
		if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "Stopped by the owner.") || !strings.Contains(comments[len(comments)-1].Body, "**failed**") {
			t.Errorf("killed run's stopped handover: %+v", comments)
		}
		if out := a.mustCmd(demo, "run", "gc", "--failed"); !strings.Contains(out, "removed 1 runs, purged 1 ended runs") {
			t.Errorf("gc killed run: %s", out)
		}

		// A record whose launcher is dead may name a harness pid since
		// reused by an unrelated process: stop refuses and signals nothing.
		deadLauncher := exec.Command("true")
		if err := deadLauncher.Run(); err != nil {
			t.Fatal(err)
		}
		unrelated := exec.Command("sleep", "300")
		unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := unrelated.Start(); err != nil {
			t.Fatal(err)
		}
		unrelatedExited := make(chan struct{})
		go func() { _ = unrelated.Wait(); close(unrelatedExited) }()
		t.Cleanup(func() { _ = unrelated.Process.Kill(); <-unrelatedExited })
		stale := map[string]any{
			"id": "r-stale", "kind": "implement", "actor": "claude", "instance": a.cfg.Name, "repo": "bottown/demo",
			"issue": stopIssue.Number, "harness": "claude", "status": "running", "started": time.Now().UTC(),
			"pid": deadLauncher.Process.Pid, "harness_pid": unrelated.Process.Pid,
		}
		staleFile := filepath.Join(demo, ".bottown", "runs", "r-stale.json")
		if b, err := json.Marshal(stale); err != nil {
			t.Fatal(err)
		} else if err := os.WriteFile(staleFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := a.cmd(demo, "run", "stop", "--grace", "1s", "r-stale"); err == nil || !strings.Contains(out, "launcher") || !strings.Contains(out, "is dead") {
			t.Errorf("run stop with a dead launcher: %v\n%s", err, out)
		}
		select {
		case <-unrelatedExited:
			t.Errorf("run stop signalled the unrelated process %d", unrelated.Process.Pid)
		case <-time.After(500 * time.Millisecond):
		}
		_ = os.Remove(staleFile)

		// A batch discuss run from main: the question reaches the prompt
		// through stdin, the agent comments, and it cannot push.
		discuss := "set -e; grep -q '## Question to discuss' \"$BOTTOWN_PROMPT_FILE\"; grep -q \"what's best: blah or foo?\" \"$BOTTOWN_PROMPT_FILE\"; " +
			"echo 'foo, because' | " + bin + " comment --body-file -; " +
			"if git push -q origin HEAD:refs/heads/discuss-push 2>/dev/null; then echo pushed; exit 9; fi; echo 'discussed by script'"
		c := exec.Command(bin, "run", "discuss", "-a", "agy", "--question-file", "-", fmt.Sprint(issue.Number))
		c.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+discuss)
		c.Dir, c.Stdin = demo, strings.NewReader("what's best: blah or foo?\n")
		if b, err := c.CombinedOutput(); err != nil || !strings.Contains(string(b), "succeeded") {
			t.Fatalf("run discuss: %v\n%s", err, b)
		}
		if out, err := a.cmd(demo, "run", "discuss", "-a", "agy", "--question-file", "-", fmt.Sprint(issue.Number), "extra"); err == nil || !strings.Contains(out, "not both") {
			t.Errorf("run discuss with two questions: %v %s", err, out)
		}
		var dc []struct {
			User struct{ Login string }
			Body string
		}
		a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue.Number), nil, &dc)
		if k := len(dc); k < 2 || dc[k-2].User.Login != "agy" || strings.TrimSpace(dc[k-2].Body) != "foo, because" ||
			!strings.Contains(dc[k-1].Body, "· discuss · agy") || !strings.Contains(dc[k-1].Body, "Question:\n> what's best: blah or foo?") ||
			!strings.Contains(dc[k-1].Body, "discussed by script") {
			t.Errorf("discuss comments on the issue: %+v", dc)
		}
		if out := a.mustCmd(demo, "run", "ls"); !strings.Contains(out, "discuss") || !strings.Contains(out, "succeeded") {
			t.Errorf("run ls after discuss:\n%s", out)
		}
	})

	t.Run("flow with a scripted harness", func(t *testing.T) {
		withHarness := func(script string, args ...string) (string, error) {
			c := exec.Command(bin, args...)
			c.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+script)
			c.Dir = demo
			out, err := c.CombinedOutput()
			return string(out), err
		}
		newIssue := func(title string) int64 {
			t.Helper()
			var issue struct{ Number int64 }
			a.mustAPI(owner, "POST", "/repos/bottown/demo/issues", map[string]string{"title": title}, &issue)
			return issue.Number
		}
		assertStopComment := func(issue int64, want string) {
			t.Helper()
			var comments []struct{ Body string }
			a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/issues/%d/comments", issue), nil, &comments)
			if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "bottown flow: "+want) {
				t.Errorf("flow stop comment on #%d, want %q: %+v", issue, want, comments)
			}
		}
		flowScript := func(dir, mode string) string {
			implementCount := filepath.Join(dir, "implements")
			verifyCount := filepath.Join(dir, "verifies")
			verdict := "event=APPROVED"
			if mode == "follow" {
				verdict = `if [ "$n" = 1 ]; then event=REQUEST_CHANGES; else event=APPROVED; fi`
			} else if mode == "reject" {
				verdict = "event=REQUEST_CHANGES"
			}
			return `set -e
if grep -Fq 'working as the **implementer**' "$BOTTOWN_PROMPT_FILE"; then
  n=$(cat ` + implementCount + ` 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > ` + implementCount + `
  echo "change $n" > flow.txt
  git add flow.txt
  git -c commit.gpgsign=false commit -qm "flow change $n"
  git push -q origin HEAD
  if [ "$n" = 1 ]; then
    ` + bin + ` mcp call pr_create '{"title":"flow PR","body":"scripted"}' > /dev/null
  fi
  echo implemented
else
  n=$(cat ` + verifyCount + ` 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > ` + verifyCount + `
  ` + verdict + `
  ` + bin + ` mcp call review_submit "{\"event\":\"$event\",\"body\":\"flow verdict\",\"commit_id\":\"$(git rev-parse HEAD)\"}" > /dev/null
  echo "$event"
fi`
		}
		readCount := func(path string) string {
			t.Helper()
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(b))
		}

		t.Run("approve first time", func(t *testing.T) {
			issue := newIssue("flow approve")
			dir := t.TempDir()
			out, err := withHarness(flowScript(dir, "approve"), "flow", fmt.Sprint(issue), "--implement", "claude", "--verify", "agy", "--timeout", "10s")
			if err != nil || !strings.Contains(out, "ready to merge") {
				t.Fatalf("flow approve: %v\n%s", err, out)
			}
			if readCount(filepath.Join(dir, "implements")) != "1" || readCount(filepath.Join(dir, "verifies")) != "1" {
				t.Error("approve-first-time used more than one round")
			}
			assertStopComment(issue, "ready to merge")
		})

		t.Run("follow-up round", func(t *testing.T) {
			issue := newIssue("flow follow-up")
			dir := t.TempDir()
			out, err := withHarness(flowScript(dir, "follow"), "flow", fmt.Sprint(issue), "--implement", "claude", "--verify", "agy", "--timeout", "10s")
			if err != nil || !strings.Contains(out, "ready to merge") {
				var prs []struct {
					Number int64
					Head   struct{ Ref, Sha string }
				}
				a.mustAPI(owner, "GET", "/repos/bottown/demo/pulls?state=open&limit=50", nil, &prs)
				for _, pr := range prs {
					if pr.Head.Ref == fmt.Sprintf("issue/%d", issue) {
						var reviews []map[string]any
						a.mustAPI(owner, "GET", fmt.Sprintf("/repos/bottown/demo/pulls/%d/reviews", pr.Number), nil, &reviews)
						t.Logf("follow-up PR !%d at %s: reviews %+v", pr.Number, pr.Head.Sha, reviews)
					}
				}
				t.Fatalf("flow follow-up: %v\n%s", err, out)
			}
			if readCount(filepath.Join(dir, "implements")) != "2" || readCount(filepath.Join(dir, "verifies")) != "2" {
				t.Error("follow-up did not use exactly two implement and verify runs")
			}
			assertStopComment(issue, "ready to merge")
		})

		t.Run("out of rounds", func(t *testing.T) {
			issue := newIssue("flow out of rounds")
			dir := t.TempDir()
			out, err := withHarness(flowScript(dir, "reject"), "flow", fmt.Sprint(issue), "--implement", "claude", "--verify", "agy", "--rounds", "2", "--timeout", "10s")
			if err == nil || !strings.Contains(out, "out of rounds") {
				t.Fatalf("flow out of rounds: %v\n%s", err, out)
			}
			if readCount(filepath.Join(dir, "implements")) != "2" || readCount(filepath.Join(dir, "verifies")) != "2" {
				t.Error("out-of-rounds used the wrong number of runs")
			}
			assertStopComment(issue, "out of rounds")
		})

		t.Run("failed run", func(t *testing.T) {
			issue := newIssue("flow failure")
			out, err := withHarness("exit 3", "flow", fmt.Sprint(issue), "--implement", "claude", "--verify", "agy", "--timeout", "10s")
			if err == nil || !strings.Contains(out, "run failed") {
				t.Fatalf("flow failure: %v\n%s", err, out)
			}
			assertStopComment(issue, "run failed")
		})

		t.Run("resume killed flow while waiting for an actor", func(t *testing.T) {
			issue := newIssue("flow resume")
			blockerIssue := newIssue("actor slot blocker")
			dir := t.TempDir()
			idFile := filepath.Join(dir, "blocker-id")
			blockerScript := `echo "$BOTTOWN_RUN_ID" > ` + idFile + `; trap 'exit 0' TERM; while :; do sleep 1; done`
			blocker := exec.Command(bin, "run", "discuss", "-a", "agy", fmt.Sprint(blockerIssue))
			blocker.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+blockerScript)
			blocker.Dir = demo
			var blockerOut bytes.Buffer
			blocker.Stdout, blocker.Stderr = &blockerOut, &blockerOut
			if err := blocker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if blocker.Process != nil {
					_ = blocker.Process.Kill()
					_ = blocker.Wait()
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			var blockerID string
			for blockerID == "" {
				b, _ := os.ReadFile(idFile)
				blockerID = strings.TrimSpace(string(b))
				if blockerID == "" {
					if time.Now().After(deadline) {
						t.Fatal("blocker did not start")
					}
					time.Sleep(100 * time.Millisecond)
				}
			}

			startFlow := func(name string) (*exec.Cmd, *os.File) {
				log, err := os.Create(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(bin, "flow", fmt.Sprint(issue), "--implement", "claude", "--verify", "agy", "--timeout", "10s")
				cmd.Env = append(a.env(), "BOTTOWN_HARNESS_CMD="+flowScript(dir, "approve"))
				cmd.Dir = demo
				cmd.Stdout, cmd.Stderr = log, log
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
					_ = log.Close()
				})
				return cmd, log
			}
			waitFor := func(name, want string) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for {
					b, _ := os.ReadFile(filepath.Join(dir, name))
					if strings.Contains(string(b), want) {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("%s did not contain %q:\n%s", name, want, b)
					}
					time.Sleep(100 * time.Millisecond)
				}
			}

			first, firstLog := startFlow("first.log")
			waitFor("first.log", "waiting for agy's actor slot")
			_ = first.Process.Kill()
			_ = first.Wait()
			_ = firstLog.Close()

			resumed, _ := startFlow("resumed.log")
			waitFor("resumed.log", "waiting for agy's actor slot")
			a.mustCmd(demo, "run", "stop", blockerID)
			if err := resumed.Wait(); err != nil {
				b, _ := os.ReadFile(filepath.Join(dir, "resumed.log"))
				t.Fatalf("resumed flow: %v\n%s", err, b)
			}
			if readCount(filepath.Join(dir, "implements")) != "1" || readCount(filepath.Join(dir, "verifies")) != "1" {
				t.Error("resume repeated the implement run or added a verify run")
			}
			assertStopComment(issue, "ready to merge")
		})

		t.Run("flow started from web ui handler", func(t *testing.T) {
			issue := newIssue("flow from web ui")
			dir := t.TempDir()

			t.Setenv("BOTTOWN_HOME", home)
			t.Setenv("BOTTOWN_SECRET_STORE", "file")
			t.Setenv("BOTTOWN_OWNER_LOGIN", "owner")
			t.Setenv("BOTTOWN_OWNER_EMAIL", "owner@e2e.test")
			t.Setenv("BOTTOWN_INSTANCE", a.name)
			t.Setenv("BOTTOWN_HARNESS_CMD", flowScript(dir, "approve"))

			srv, err := webui.NewServer(webui.Config{
				Instance:    a.cfg,
				RepoTop:     demo,
				FullRepo:    "bottown/demo",
				OwnerKey:    owner,
				BottownPath: bin,
			})
			if err != nil {
				t.Fatalf("webui server: %v", err)
			}
			defer srv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = srv.Serve(ctx) }()

			out, err := srv.ExecuteFlow(context.Background(), webui.FlowRequest{
				Issue:     issue,
				Implement: "claude",
				Verify:    "agy",
				Timeout:   "20s",
			})
			if err != nil {
				t.Fatalf("start flow from ui: %v", err)
			}
			if !strings.Contains(out, fmt.Sprintf("Flow started for issue #%d", issue)) {
				t.Fatalf("unexpected flow output: %s", out)
			}

			deadline := time.Now().Add(30 * time.Second)
			logFile := filepath.Join(demo, ".bottown", fmt.Sprintf("flow-%d.log", issue))
			for {
				b, _ := os.ReadFile(logFile)
				if strings.Contains(string(b), "ready to merge") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("flow did not reach ready to merge within deadline; log:\n%s", b)
				}
				time.Sleep(200 * time.Millisecond)
			}

			assertStopComment(issue, "ready to merge")
		})
	})

	t.Run("git gc-branches deletes only unguarded merged branches", func(t *testing.T) {
		remote := "bottown-" + a.name
		wt := filepath.Join(fam, "claude")
		// Fetches and pushes from main use the owner's credential helper,
		// which needs the instance's environment.
		ownerGit := func(args ...string) {
			t.Helper()
			c := exec.Command("git", append([]string{"-C", demo}, args...)...)
			c.Env = a.env()
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
		git(t, demo, "checkout", "-q", "main")
		ownerGit("fetch", "-q", remote)
		tip := git(t, demo, "rev-parse", remote+"/main")
		older := git(t, demo, "rev-parse", remote+"/main~1")
		// 901 merged; 903 merged and checked out in claude's worktree; 904
		// merged but the head of an open PR (into side, which it is ahead
		// of); 902 has a commit main lacks.
		ownerGit("push", "-q", remote, tip+":refs/heads/issue/901", tip+":refs/heads/issue/903", tip+":refs/heads/issue/904", older+":refs/heads/side")
		git(t, demo, "switch", "-q", "-c", "issue/902", tip)
		os.WriteFile(filepath.Join(demo, "gc.txt"), []byte("unmerged\n"), 0o644)
		git(t, demo, "add", "gc.txt")
		git(t, demo, "commit", "-q", "-m", "unmerged")
		ownerGit("push", "-q", remote, "issue/902")
		git(t, demo, "switch", "-q", "main")
		git(t, demo, "branch", "-q", "issue/901", tip) // a merged local branch too
		ownerGit("fetch", "-q", remote)
		git(t, wt, "switch", "-q", "-c", "issue/903", "--no-track", remote+"/issue/903")
		defer git(t, wt, "switch", "-q", "--detach")
		var pr struct{ Number int64 }
		a.mustAPI(owner, "POST", "/repos/bottown/demo/pulls", map[string]string{"head": "issue/904", "base": "side", "title": "gc guard"}, &pr)

		exists := func(ref string) bool {
			return exec.Command("git", "-C", demo, "rev-parse", "-q", "--verify", ref).Run() == nil
		}
		if out, err := a.cmd(wt, "git", "gc-branches"); err == nil || !strings.Contains(out, "main checkout") {
			t.Errorf("gc-branches in an actor worktree: %v %s", err, out)
		}
		out := a.mustCmd(demo, "git", "gc-branches", "--dry-run")
		for _, want := range []string{
			"would delete " + remote + "/issue/901",
			"would delete issue/901 (local)",
			// git may report the worktree through a symlink (/private/var).
			"kept issue/903: checked out in ",
			filepath.Join(filepath.Base(fam), "claude") + "\n",
			fmt.Sprintf("kept issue/904: open pull request !%d", pr.Number),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("dry run lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "issue/902") || !exists("refs/remotes/"+remote+"/issue/901") || !exists("refs/heads/issue/901") {
			t.Fatalf("dry run deleted or listed the wrong branches:\n%s", out)
		}

		out = a.mustCmd(demo, "git", "gc-branches")
		if !strings.Contains(out, "deleted 1 remote and 1 local branches") {
			t.Errorf("gc-branches:\n%s", out)
		}
		var branches []struct{ Name string }
		a.mustAPI(owner, "GET", "/repos/bottown/demo/branches?limit=50", nil, &branches)
		on := map[string]bool{}
		for _, b := range branches {
			on[b.Name] = true
		}
		if on["issue/901"] || !on["issue/902"] || !on["issue/903"] || !on["issue/904"] || !on["main"] {
			t.Errorf("forge branches after gc: %v", on)
		}
		// Refs are shared with actor worktrees: one pass prunes the fam.
		if exists("refs/heads/issue/901") || exists("refs/remotes/"+remote+"/issue/901") || !exists("refs/heads/issue/902") || !exists("refs/heads/issue/903") {
			t.Errorf("local refs after gc are wrong")
		}
		if got := git(t, wt, "branch", "--show-current"); got != "issue/903" {
			t.Errorf("claude's worktree switched to %q", got)
		}
	})

	t.Run("restart keeps state and reconcile is a no-op", func(t *testing.T) {
		if out, err := exec.Command("docker", "restart", a.cfg.Container()).CombinedOutput(); err != nil {
			t.Fatalf("docker restart: %v %s", err, out)
		}
		if out := a.up(); !strings.Contains(out, "no provisioning changes") {
			t.Fatalf("up after restart:\n%s", out)
		}
		a.mustAPI(owner, "GET", "/repos/bottown/demo", nil, nil)
	})

	t.Run("no forgejo token on the host", func(t *testing.T) {
		raw, err := exec.Command("docker", "exec", "-u", "git", a.cfg.Container(), "cat", "/data/bottown/secrets.json").Output()
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			AdminToken string `json:"admin_token"`
			Tokens     map[string]struct{ Token string }
		}
		json.Unmarshal(raw, &st)
		secrets := []string{st.AdminToken}
		for _, tk := range st.Tokens {
			secrets = append(secrets, tk.Token)
		}
		filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, s := range secrets {
				if s != "" && bytes.Contains(b, []byte(s)) {
					t.Errorf("forgejo token found in %s", p)
				}
			}
			return nil
		})
	})

	t.Run("upgrade in place on the same volume", func(t *testing.T) {
		tag := "bottown:" + a.name + "-next"
		c := exec.Command("docker", "build", "-q", "-f", "docker/Dockerfile", "-t", tag, "--build-arg", "VERSION=next", ".")
		c.Dir = src
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
		t.Cleanup(func() { exec.Command("docker", "rmi", "-f", tag).Run() })
		out := a.up("--image", tag)
		if !strings.Contains(out, "recreating") || !strings.Contains(out, "no provisioning changes") {
			t.Fatalf("upgrade:\n%s", out)
		}
		if h := a.health(); h["version"] != "next" {
			t.Fatalf("version after upgrade = %v", h["version"])
		}
		a.mustAPI(owner, "GET", "/repos/bottown/demo", nil, nil)
	})

	t.Run("two instances side by side", func(t *testing.T) {
		b := newInstance(t)
		b.up()
		if b.cfg.ServerPort == a.cfg.ServerPort || b.cfg.Volume() == a.cfg.Volume() {
			t.Fatalf("instances collide: %+v %+v", a.cfg, b.cfg)
		}
		if code, _ := b.api(owner, "GET", "/user", nil); code != 401 {
			t.Fatalf("instance A key on B: %d", code)
		}
		a.mustCmd("", "down", "--purge")
		if h := b.health(); h["reconciled"] != true {
			t.Fatalf("B unhealthy after purging A: %v", h)
		}
	})
}

type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}
