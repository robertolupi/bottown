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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/robertolupi/bottown/internal/config"
)

var (
	bin  string // built bottown binary
	home string // BOTTOWN_HOME
	src  string // repo root
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bottown-e2e-")
	if err != nil {
		panic(err)
	}
	_, file, _, _ := runtime.Caller(0)
	src = filepath.Dir(filepath.Dir(file))
	bin = filepath.Join(dir, "bottown")
	if out, err := exec.Command("go", "build", "-o", bin, filepath.Join(src, "cmd", "bottown")).CombinedOutput(); err != nil {
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
	demo := t.TempDir()
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

		ui := strings.TrimSpace(a.mustCmd("", "web", "ui", "--print"))
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
	impl := a.mustCmd("", "credential", "key", "claude")
	impl = strings.TrimSpace(impl)
	ver := strings.TrimSpace(a.mustCmd("", "credential", "key", "codex"))

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
		if code, _ := a.api(ik, "GET", "/repos/bottown/elsewhere", nil); code != 403 {
			t.Errorf("implement run read other repo: %d, want 403", code)
		}
		if code, _ := runs(ik, "POST", "/v1/runs", map[string]any{"kind": "implement", "actor": "claude", "repo": "demo", "issue": issue.Number}); code != 403 {
			t.Errorf("run key creating runs: %d, want 403", code)
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
		if code, m := verify("claude2", true); code != 201 || m["allow_same_family"] != true {
			t.Errorf("same-family verify with override: %d %v", code, m)
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
		if code, m := runs(owner, "DELETE", "/v1/runs/"+impl["id"].(string), map[string]string{"status": "succeeded"}); code != 200 || m["status"] != "succeeded" {
			t.Fatalf("end run: %d %v", code, m)
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
		call := func(sess *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
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
		ver2 := newRun(map[string]any{"kind": "verify", "actor": "codex", "repo": "demo", "pr": prn})
		vs2 := connect(a.cfg.ServerURL()+"/mcp", ver2["key"].(string))
		rv, errMsg := call(vs2, "review_submit", map[string]any{"event": "APPROVED", "body": "verified", "commit_id": ver2["sha"]})
		if errMsg != "" || rv["commit_id"] != ver2["sha"] || !strings.Contains(fmt.Sprint(rv["body"]), "bottown verify run "+ver2["id"].(string)) {
			t.Fatalf("review_submit: %v %s", rv, errMsg)
		}
		if c, errMsg := call(vs2, "comment", map[string]any{"body": "handover"}); errMsg != "" || c["author"] != "codex" {
			t.Fatalf("comment: %v %s", c, errMsg)
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
