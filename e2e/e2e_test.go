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
		for _, u := range []string{"owner", "implementer", "verifier", "manager"} {
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

	impl := a.mustCmd("", "credential", "key", "implementer")
	impl = strings.TrimSpace(impl)
	ver := strings.TrimSpace(a.mustCmd("", "credential", "key", "verifier"))

	t.Run("policy and protection bound the bots", func(t *testing.T) {
		head := git(t, demo, "rev-parse", "HEAD")
		if code, b := a.api(impl, "POST", "/repos/bottown/demo/statuses/"+head, map[string]string{"state": "success", "context": "bottown/verify"}); code != 403 {
			t.Errorf("implementer commit status: %d %s", code, b)
		}
		if code, _ := a.api(impl, "GET", "/repos/bottown/demo/issues", nil); code != 200 {
			t.Errorf("implementer read issues: %d", code)
		}
		if out, err := a.pushAs(demo, "verifier", ver, "main:refs/heads/verifier-branch"); err == nil {
			t.Errorf("verifier push succeeded:\n%s", out)
		}
		if out, err := a.pushAs(demo, "implementer", impl, "main:refs/heads/main"); err == nil && !strings.Contains(out, "Everything up-to-date") {
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
		if me.Login != "implementer" {
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
		if out, err := a.pushAs(demo, "implementer", impl, "feature"); err != nil {
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
		if out, err := a.pushAs(demo, "implementer", impl, "feature"); err != nil {
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
		if !strings.Contains(string(b), `"actor":"implementer"`) {
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
