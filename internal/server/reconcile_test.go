package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

type bearer struct {
	key  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.base.RoundTrip(r)
}

// A reconcile must not wait for requests that stay open (an MCP session's
// event stream, a slow git transfer), and requests must not wait for a
// reconcile: issue #229, where one queued reconcile froze every
// authenticated request.
func TestReconcileWithLongLivedRequests(t *testing.T) {
	gitStarted, gitRelease := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":1,"login":"bottown-admin"}`))
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(gitStarted)
			<-gitRelease
		default:
			http.Error(w, "fake forgejo", http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()
	defer close(gitRelease)
	fu, _ := url.Parse(upstream.URL)

	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Update(func(st *secrets.State) error {
		st.AdminToken = "tok_admin"
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}}
		return nil
	})
	ownerKey, _ := store.NewAPIKey("owner", "test", "")
	actorKey, _ := store.NewAPIKey("claude", "test", "")
	desiredPath := filepath.Join(t.TempDir(), "bottown.toml")
	if err := os.WriteFile(desiredPath, []byte("org = \"reloaded\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &provision.Reconciler{BaseURL: upstream.URL, Store: store, DesiredPath: desiredPath,
		Owner: provision.Owner{Login: "owner"}, Desired: config.Desired{Org: "bottown"}}
	srv := &Server{Owner: "owner", Store: store, Log: slog.Default(), Rec: rec, ForgejoURL: fu}
	srv.health.Reconciled = true
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// An MCP session: after initialize the client keeps a GET event stream
	// open for as long as the session lives.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{actorKey, http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// A git request that stays open until the test ends.
	go func() {
		req, _ := http.NewRequest("GET", ts.URL+"/git/bottown/demo.git/info/refs?service=git-upload-pack", nil)
		req.Header.Set("Authorization", "Bearer "+actorKey)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-gitStarted:
	case <-ctx.Done():
		t.Fatal("git request never reached upstream")
	}

	do := func(method, path, key string) int {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		req, _ := http.NewRequestWithContext(rctx, method, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// The fake Forgejo fails the pass after the reload; what matters is
	// that it ran instead of waiting for the open requests.
	if code := do("POST", "/v1/reconcile", ownerKey); code != http.StatusBadGateway {
		t.Errorf("reconcile: %d, want 502 from the fake forgejo", code)
	}
	if got := rec.Spec().Org; got != "reloaded" {
		t.Errorf("desired org after reconcile: %q, want the reloaded one", got)
	}
	if code := do("GET", "/v1/whoami", actorKey); code != http.StatusOK {
		t.Errorf("whoami during open requests: %d", code)
	}
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"}); err != nil {
		t.Errorf("MCP tool call after reconcile: %v", err)
	}
}

// A reconcile that cannot get the slot before its client gives up answers
// 503 instead of staying queued.
func TestReconcileBusy(t *testing.T) {
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, _ := store.NewAPIKey("owner", "test", "")
	actorKey, _ := store.NewAPIKey("claude", "test", "")
	hold := make(chan struct{})
	rec := &provision.Reconciler{Store: store, Desired: config.Desired{Org: "bottown"},
		Admin: &blockingAdmin{hold: hold}}
	srv := &Server{Owner: "owner", Store: store, Log: slog.Default(), Rec: rec}
	srv.health.Reconciled = true
	h := srv.Handler()

	// A first pass holds the slot, blocked in Bootstrap.
	first, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.Run(first)
	}()
	<-hold
	defer func() { stop(); <-done }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", "/v1/reconcile", nil)
	req.Header.Set("Authorization", "Bearer "+ownerKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("reconcile while another runs: %d %s, want 503", w.Code, w.Body)
	}

	// Readers do not wait for the running pass.
	req = httptest.NewRequest("GET", "/v1/actors", nil)
	req.Header.Set("Authorization", "Bearer "+actorKey)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("actors during a pass: %d", w.Code)
	}
}

// blockingAdmin blocks CreateAdmin until its context ends.
type blockingAdmin struct {
	hold chan struct{}
	once sync.Once
}

func (b *blockingAdmin) CreateAdmin(ctx context.Context, _, _, _ string) error {
	b.once.Do(func() { close(b.hold) })
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingAdmin) GenerateToken(context.Context, string, string, []string) (string, error) {
	return "", errors.New("unused")
}

func (b *blockingAdmin) ChangePassword(context.Context, string, string) error { return nil }
