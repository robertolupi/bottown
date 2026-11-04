package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/hostctl"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

func TestPostHealthFailsWhenServerNeverAnswers(t *testing.T) {
	// A server that accepts the connection but never answers: paths in answer
	// reply at once, everything else blocks until the client gives up.
	serve := func(answer map[string]string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body, ok := answer[r.URL.Path]; ok {
				_, _ = io.WriteString(w, body)
				return
			}
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	instance := func(t *testing.T, srv *httptest.Server) config.Instance {
		t.Helper()
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOTTOWN_HOME", t.TempDir())
		t.Setenv("BOTTOWN_SECRET_STORE", "file")
		inst := config.Instance{Name: "e2e-stall", ServerPort: port, OwnerLogin: "owner"}
		dir, err := config.InstanceDir(inst.Name)
		if err != nil {
			t.Fatal(err)
		}
		// A stored key makes handOff check it with whoami first, as up does.
		if err := hostctl.OpenSecrets(inst.Name, dir).Set(hostctl.AccountAPIKey, "k"); err != nil {
			t.Fatal(err)
		}
		return inst
	}

	for _, c := range []struct {
		name      string
		answer    map[string]string
		reconcile bool
		want      string
	}{
		{"whoami hangs", nil, false, "checking the owner key"},
		{"reconcile hangs", map[string]string{"/v1/whoami": `{"Actor":"owner"}`}, true, "reconciling provisioning"},
	} {
		t.Run(c.name, func(t *testing.T) {
			inst := instance(t, serve(c.answer))
			g := &globals{instance: inst.Name}
			var h *server.Health
			if c.reconcile {
				h = &server.Health{}
			}
			start := time.Now()
			_, err := postHealth(context.Background(), g, hostctl.Docker{}, inst, h, 200*time.Millisecond)
			if err == nil {
				t.Fatal("postHealth returned no error on a server that never answers")
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("postHealth took %s, want it to give up near the 200ms timeout", elapsed)
			}
			for _, want := range []string{"gave up after", c.want, "/healthz", "docker logs"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestOwnerEmail(t *testing.T) {
	for _, c := range []struct{ env, git, want string }{
		{"", "me@example.com", "me@example.com"},
		{"", "", ""},
		{"", "claude@bottown.local", ""},
		{"", "Kimi@Bottown.Local", ""},
		{"owner@e2e.test", "claude@bottown.local", "owner@e2e.test"},
		{"owner@e2e.test", "me@example.com", "owner@e2e.test"},
	} {
		if got := ownerEmail(c.env, c.git); got != c.want {
			t.Errorf("ownerEmail(%q, %q) = %q, want %q", c.env, c.git, got, c.want)
		}
	}
}

func TestSameImageSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	src := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", src, "-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	n := 0
	commit := func(file string) string {
		t.Helper()
		n++
		p := filepath.Join(src, file)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644)
		git("add", ".")
		git("commit", "-qm", file)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module "+modulePath+"\n"), 0o644)
	base := commit("main.go")
	docs := commit("docs/index.md")
	commit("README.md")
	readmes := commit("docker/README.md")
	prompt := commit("internal/runs/prompts/implement.md")
	code := commit("main.go")

	img := func(sha string) string { return "bottown:" + sha[:12] }
	for _, c := range []struct {
		image, ref string
		want       bool
	}{
		{img(base), base, true},
		{img(base), docs, true},
		{img(base), readmes, true},
		{img(base), prompt, false}, // embedded in the binary
		{img(readmes), code, false},
		{img(code), code, true},
		{img(code), "HEAD", true},
		{"bottown:prod-wt", code, false},
		{"bottown:main", code, false},
		{"other:" + code[:12], code, false},
		{"", code, false},
		{img(base), "nosuchref", false},
	} {
		if got := sameImageSource(src, c.image, c.ref); got != c.want {
			t.Errorf("sameImageSource(%s, %.12s) = %v, want %v", c.image, c.ref, got, c.want)
		}
	}
}

func TestUpDownRefuseInRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("BOTTOWN_RUN_FILE", "")
	t.Setenv("BOTTOWN_INSTANCE", "")
	t.Setenv("BOTTOWN_HOME", t.TempDir())
	dir := t.TempDir()
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	runDir := filepath.Join(dir, "main", ".bottown", "runs", "r-test")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(runDir, "init", "-q")

	actorDir := filepath.Join(dir, "claude")
	if err := os.MkdirAll(actorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(actorDir, "init", "-q")
	mustGit(actorDir, "config", "extensions.worktreeConfig", "true")
	mustGit(actorDir, "config", "--worktree", gitActorKey, "claude")
	keyDir := filepath.Join(actorDir, ".git", "bottown")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRun := func(rec runs.Record) {
		t.Helper()
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(keyDir, "run.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRun(runs.Record{ID: "r-live", Actor: "claude", Key: "k"})

	for _, c := range []struct {
		name, dir string
		args      []string
		want      string
	}{
		{"up in a run worktree", runDir, []string{"up", "-i", "dev"}, "this is a run worktree: `bottown up` would change instance dev"},
		{"down in a run worktree", runDir, []string{"down", "-i", "prod"}, "this is a run worktree: `bottown down` would change instance prod"},
		{"up in an actor worktree during a run", actorDir, []string{"up", "-i", "dev"}, "actor claude's worktree is in run r-live"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(c.dir)
			root := newRoot()
			root.SetArgs(c.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := root.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}

	t.Run("run file set", func(t *testing.T) {
		t.Setenv("BOTTOWN_RUN_FILE", filepath.Join(dir, "run.json"))
		if err := refuseInRun("up", "dev"); err == nil || !strings.Contains(err.Error(), "this is a bottown run") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("e2e instances allowed", func(t *testing.T) {
		t.Chdir(runDir)
		if err := refuseInRun("down", "e2e-abc123"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ended run in actor worktree allowed", func(t *testing.T) {
		writeRun(runs.Record{ID: "r-live", Actor: "claude"})
		t.Chdir(actorDir)
		if err := refuseInRun("up", "dev"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunNewContainerRepicksTakenPorts(t *testing.T) {
	t.Setenv("BOTTOWN_HOME", t.TempDir())
	all := func(int) bool { return true }
	inst := config.Instance{Name: "e2e-a", ForgejoPort: 3050, ServerPort: 3051}
	if err := config.SaveInstance(inst); err != nil {
		t.Fatal(err)
	}
	// Another process bound 3050, then 3053, after they were picked here.
	taken := map[int]bool{3050: true, 3053: true}
	var ran []int
	discarded := 0
	run := func(i config.Instance) error {
		ran = append(ran, i.ForgejoPort)
		if taken[i.ForgejoPort] || taken[i.ServerPort] {
			return errors.New("docker run: exit status 125: Bind for 127.0.0.1:3050 failed: port is already allocated")
		}
		return nil
	}
	got, err := runNewContainer(inst, run, func() { discarded++ }, all)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForgejoPort != 3054 || got.ServerPort != 3055 || discarded != 2 || len(ran) != 3 {
		t.Fatalf("got %d/%d after runs %v, %d discarded", got.ForgejoPort, got.ServerPort, ran, discarded)
	}
	if saved, _, _ := config.LoadInstance("e2e-a"); saved.ForgejoPort != 3054 || saved.ServerPort != 3055 {
		t.Fatalf("saved ports %d/%d, want 3054/3055", saved.ForgejoPort, saved.ServerPort)
	}

	// Other errors are returned as they are, without a retry.
	n := 0
	fail := func(config.Instance) error { n++; return errors.New("docker run: no such image") }
	if _, err := runNewContainer(inst, fail, func() {}, all); err == nil || n != 1 {
		t.Fatalf("err %v after %d runs", err, n)
	}

	// The retries are bounded.
	n = 0
	busy := func(config.Instance) error { n++; return errors.New("port is already allocated") }
	if _, err := runNewContainer(inst, busy, func() {}, all); err == nil || n != portRetries+1 {
		t.Fatalf("err %v after %d runs", err, n)
	}
}
