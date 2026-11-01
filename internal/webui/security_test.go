package webui

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robertolupi/bottown/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := Config{
		Instance:   config.Instance{Name: "test-inst", ServerPort: 3031, ForgejoPort: 3030},
		RepoTop:    t.TempDir(),
		FullRepo:   "testorg/testrepo",
		OwnerKey:   "testkey",
		Port:       0, // random free port
		CLIVersion: "test-ver",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = s.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
	})
	return s
}

func TestSecurity_OneTimeTokenAndCookie(t *testing.T) {
	s := newTestServer(t)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Do not follow redirects automatically so we can inspect Set-Cookie
			return http.ErrUseLastResponse
		},
	}

	// 1. Initial request without token or cookie -> 401 Unauthorized
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without auth, got %d", resp.StatusCode)
	}

	// 2. Request with invalid token -> 401 Unauthorized
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/?token=bogus", s.Port()), nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /?token=bogus: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for bogus token, got %d", resp.StatusCode)
	}

	// 3. Request with valid one-time token -> 303 See Other with Set-Cookie
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.Port(), s.oneTimeToken), nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /?token=valid: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other after valid token, got %d", resp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == s.cookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected Set-Cookie %s in response", s.cookieName)
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("cookie must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie must have SameSite=Strict")
	}

	// 4. Try redeeming the same token again -> 401 Unauthorized (one-time use)
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.Port(), s.oneTimeToken), nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /?token=valid second time: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized on reused token, got %d", resp.StatusCode)
	}

	// 5. Subsequent request with session cookie -> 200 OK
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	req.AddCookie(sessionCookie)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET / with cookie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with valid cookie, got %d", resp.StatusCode)
	}
}

func TestSecurity_HostCheck(t *testing.T) {
	s := newTestServer(t)

	client := &http.Client{}

	// Valid Host: 127.0.0.1:<port>
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	req.Host = fmt.Sprintf("127.0.0.1:%d", s.Port())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("valid host request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for 127.0.0.1 host, got %d", resp.StatusCode)
	}

	// Valid Host: localhost:<port>
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	req.Host = fmt.Sprintf("localhost:%d", s.Port())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("valid localhost request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for localhost host, got %d", resp.StatusCode)
	}

	// Invalid Host: evil.com (DNS rebinding attack)
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	req.Host = "evil.com"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("evil.com host request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for evil.com host, got %d", resp.StatusCode)
	}
}

func TestSecurity_OriginCheck(t *testing.T) {
	s := newTestServer(t)
	client := &http.Client{}

	// 1. POST with valid Origin -> allowed
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/actions/refresh", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", s.Port()))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST with valid origin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for valid origin, got %d", resp.StatusCode)
	}

	// 2. POST with evil Origin -> 403 Forbidden
	req, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/actions/refresh", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	req.Header.Set("Origin", "http://attacker.example.com")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST with evil origin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for evil origin, got %d", resp.StatusCode)
	}

	// 3. POST without Origin or Referer -> 403 Forbidden
	req, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/actions/refresh", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST without origin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without origin/referer, got %d", resp.StatusCode)
	}
}

func TestSecurity_CSPHeader(t *testing.T) {
	s := newTestServer(t)
	client := &http.Client{}

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", s.Port()), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatalf("missing Content-Security-Policy header")
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP must enforce script-src 'self' (no inline scripts): %s", csp)
	}
	if strings.Contains(csp, "'unsafe-inline'") && strings.Contains(csp, "script-src") && strings.Index(csp, "'unsafe-inline'") < strings.Index(csp, "style-src") {
		t.Errorf("CSP must not permit unsafe-inline scripts: %s", csp)
	}
}

// The built assets have fixed names (web/vite.config.ts): browsers must
// revalidate them, and unknown paths fall back to index.html.
func TestStaticHandler_Built(t *testing.T) {
	ui := uiFS(fstest.MapFS{
		"dist/README.md":           {Data: []byte("placeholder")},
		"dist/ui/index.html":       {Data: []byte(`<script src="/assets/index.js"></script>`)},
		"dist/ui/assets/index.js":  {Data: []byte("console.log(1)")},
		"dist/ui/assets/index.css": {Data: []byte("body{}")},
	})
	if ui == nil {
		t.Fatal("uiFS: nil for a built dist/ui")
	}
	h := staticHandler(ui)
	for path, want := range map[string]string{
		"/":                 "/assets/index.js",
		"/runs":             "/assets/index.js",
		"/assets/index.js":  "console.log(1)",
		"/assets/index.css": "body{}",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: %d %q, want 200 containing %q", path, rec.Code, rec.Body.String(), want)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want no-cache", path, cc)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/nope: %d, want 404", rec.Code)
	}
}

// dist/ui is not tracked: a binary built without `make web` serves a stub
// page that says how to build the UI.
func TestStaticHandler_NotBuilt(t *testing.T) {
	if ui := uiFS(fstest.MapFS{"dist/README.md": {Data: []byte("placeholder")}}); ui != nil {
		t.Fatal("uiFS: non-nil without dist/ui/index.html")
	}
	h := staticHandler(nil)
	for _, path := range []string{"/", "/runs", "/assets/index.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "make web") {
			t.Errorf("GET %s: %d %q, want 200 stub page", path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: Content-Type = %q, want text/html", path, ct)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/nope: %d, want 404", rec.Code)
	}
}

// When this checkout ran `make web`, the embedded index.html references
// the fixed asset names.
func TestStaticAssets_FixedNames(t *testing.T) {
	ui := uiFS(distFS)
	if ui == nil {
		t.Skip("dist/ui not built (make web)")
	}
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{`src="/assets/index.js"`, `href="/assets/index.css"`} {
		if !strings.Contains(string(index), ref) {
			t.Errorf("index.html does not reference %s:\n%s", ref, index)
		}
	}
}

func TestPush_Validation(t *testing.T) {
	s := newTestServer(t)
	client := &http.Client{}

	pushURL := fmt.Sprintf("http://127.0.0.1:%d/api/actions/push", s.Port())

	doPost := func(body io.Reader) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, pushURL, body)
		req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
		req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", s.Port()))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST push: %v", err)
		}
		return resp
	}

	// 1. Missing body -> 400
	resp := doPost(nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for nil body, got %d", resp.StatusCode)
	}

	// 2. Empty / invalid JSON -> 400
	resp = doPost(strings.NewReader("{invalid-json"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON body, got %d", resp.StatusCode)
	}

	// 3. Missing head field -> 400
	resp = doPost(strings.NewReader(`{}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty head field, got %d", resp.StatusCode)
	}

	// 4. Blank head field -> 400
	resp = doPost(strings.NewReader(`{"head": "   "}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for blank head field, got %d", resp.StatusCode)
	}
}
