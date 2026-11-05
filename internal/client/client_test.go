package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/server"
)

func TestCallSendsKeyAndJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"a":1}` {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"b":2}`)
	}))
	defer srv.Close()
	var out struct{ B int }
	if err := New(srv.URL+"/", "k").Call(context.Background(), "POST", "/x", map[string]int{"a": 1}, &out); err != nil || out.B != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var raw []byte
	if err := New(srv.URL, "k").Call(context.Background(), "POST", "/x", map[string]int{"a": 1}, &raw); err != nil || string(raw) != `{"b":2}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}

func TestCallWithoutKeySendsNoAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("Authorization sent without a key")
		}
	}))
	defer srv.Close()
	if err := New(srv.URL, "").Call(context.Background(), "GET", "/healthz", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAPIError(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int
		body    string
		msg     string
		is, not []error
	}{
		{"bottown error", 404, `{"error":"issue #3 not found"}`, "GET /p?q=1: issue #3 not found", []error{ErrNotFound}, []error{ErrForbidden}},
		{"forgejo message", 403, `{"message":"token scope"}`, "GET /p?q=1: token scope", []error{ErrForbidden}, []error{ErrNotFound}},
		{"plain body", 401, "nope\n", "GET /p?q=1: nope", []error{ErrUnauthorized}, nil},
		{"empty body", 502, "", "GET /p?q=1: 502 Bad Gateway", nil, []error{ErrNotFound, ErrForbidden, ErrUnauthorized}},
		{"actor busy", 409, `{"error":"actor claude already has a live batch run: r-1","code":"` + server.ErrorCodeActorBusy + `"}`,
			"GET /p?q=1: actor claude already has a live batch run: r-1", []error{ErrActorBusy}, []error{ErrHeadMoved}},
		{"head moved", 409, `{"error":"pull request #4: the head moved","code":"` + server.ErrorCodeHeadMoved + `"}`,
			"GET /p?q=1: pull request #4: the head moved", []error{ErrHeadMoved}, []error{ErrActorBusy}},
		// The message alone is not the code: only the server's code counts.
		{"message without code", 409, `{"error":"the head moved"}`, "GET /p?q=1: the head moved", nil, []error{ErrHeadMoved}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()
			err := New(srv.URL, "k").Call(context.Background(), "GET", "/p?q=1", nil, nil)
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want an *APIError", err)
			}
			if ae.Status != c.status || ae.Method != "GET" || ae.Path != "/p?q=1" {
				t.Errorf("APIError = %+v", ae)
			}
			if err.Error() != c.msg {
				t.Errorf("Error() = %q, want %q", err, c.msg)
			}
			for _, s := range c.is {
				if !errors.Is(err, s) {
					t.Errorf("errors.Is(err, %v) = false", s)
				}
			}
			for _, s := range c.not {
				if errors.Is(err, s) {
					t.Errorf("errors.Is(err, %v) = true", s)
				}
			}
		})
	}
}

func TestDoLeavesStatusToCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"gone"}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL, "k").Do(context.Background(), "GET", "/a?b=c", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	err = Error(resp)
	if !errors.Is(err, ErrNotFound) || err.Error() != "GET /a?b=c: gone" {
		t.Fatalf("Error(resp) = %v", err)
	}
}

func TestCallTimesOutOnAServerThatNeverAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	start := time.Now()
	err := New(srv.URL, "k").WithTimeout(100*time.Millisecond).Call(context.Background(), "GET", "/v1/whoami", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Fatalf("a timeout is not an *APIError: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("call took %s", d)
	}

	// The context bounds a call too.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := New(srv.URL, "k").Call(ctx, "GET", "/v1/whoami", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
