package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/robertolupi/bottown/internal/milestone"
)

func TestProxyForge(t *testing.T) {
	var deps []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/forgejo/api/v1/repos/o/r/issues" && r.Method == "GET":
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("type") != "issues" {
				t.Errorf("issues query %v", r.URL.Query())
			}
			// Two pages, one with a pull request in it, then an empty one.
			switch r.URL.Query().Get("page") {
			case "1":
				fmt.Fprint(w, `[{"number":1,"title":"a","state":"open","user":{"login":"owner"},"milestone":{"id":7,"title":"M","state":"closed"}},{"number":2,"pull_request":{}}]`)
			case "2":
				fmt.Fprint(w, `[{"number":3,"state":"closed","user":{"login":"agy"}}]`)
			default:
				fmt.Fprint(w, `[]`)
			}
		case p == "/forgejo/api/v1/repos/o/r/raw/docs/a b.md":
			if r.URL.Query().Get("ref") != "abc" {
				t.Errorf("raw ref %q", r.URL.Query().Get("ref"))
			}
			fmt.Fprint(w, "content")
		case p == "/forgejo/api/v1/repos/o/r/issues/5/dependencies":
			b, _ := io.ReadAll(r.Body)
			deps = append(deps, r.Method+" "+string(b))
			if r.Method == "GET" {
				if r.URL.Query().Get("page") == "1" {
					fmt.Fprint(w, `[{"number":3},{"number":4}]`)
				} else {
					fmt.Fprint(w, `[]`)
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	f := newProxyForge(srv.URL, "k", "o", "r")

	issues, err := f.Issues(ctx)
	if err != nil || len(issues) != 2 {
		t.Fatalf("Issues: %v %+v", err, issues)
	}
	if a := issues[0]; a.Number != 1 || a.Poster != "owner" || a.Closed || a.Milestone == nil || a.Milestone.ID != 7 || !a.Milestone.Closed {
		t.Errorf("issue 1: %+v %+v", a, a.Milestone)
	}
	if c := issues[1]; c.Number != 3 || !c.Closed || c.Milestone != nil {
		t.Errorf("issue 3: %+v", c)
	}
	if b, err := f.ReadFile(ctx, "abc", "docs/a b.md"); err != nil || string(b) != "content" {
		t.Errorf("ReadFile: %q %v", b, err)
	}
	if _, err := f.ReadFile(ctx, "abc", "missing.md"); !errors.Is(err, milestone.ErrNotFound) {
		t.Errorf("ReadFile of a missing file: %v", err)
	}
	if _, err := f.Issue(ctx, 99); !errors.Is(err, milestone.ErrNotFound) {
		t.Errorf("Issue of a missing issue: %v", err)
	}
	if ns, err := f.BlockedBy(ctx, 5); err != nil || !slices.Equal(ns, []int64{3, 4}) {
		t.Errorf("BlockedBy: %v %v", ns, err)
	}
	if err := f.AddBlockedBy(ctx, 5, 8); err != nil {
		t.Error(err)
	}
	if err := f.RemoveBlockedBy(ctx, 5, 3); err != nil {
		t.Error(err)
	}
	meta := func(n int64) string {
		b, _ := json.Marshal(map[string]any{"owner": "o", "repo": "r", "index": n})
		return string(b)
	}
	if want := []string{"GET ", "GET ", "POST " + meta(8), "DELETE " + meta(3)}; !slices.Equal(deps, want) {
		t.Errorf("dependency calls %q, want %q", deps, want)
	}
}
