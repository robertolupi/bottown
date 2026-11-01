package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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

// The built assets have fixed names (web/vite.config.ts): index.html must
// reference them, they must be embedded, and browsers must revalidate them.
func TestStaticAssets_FixedNames(t *testing.T) {
	s := newTestServer(t)
	client := &http.Client{}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", s.Port(), path), nil)
		req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	resp, index := get("/")
	for _, ref := range []string{`src="/assets/index.js"`, `href="/assets/index.css"`} {
		if !strings.Contains(index, ref) {
			t.Errorf("index.html does not reference %s:\n%s", ref, index)
		}
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET /: Cache-Control = %q, want no-cache", cc)
	}
	for path, ctype := range map[string]string{"/assets/index.js": "javascript", "/assets/index.css": "text/css"} {
		resp, _ := get(path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), ctype) {
			t.Errorf("GET %s: %d %s, want 200 %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), ctype)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want no-cache", path, cc)
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
