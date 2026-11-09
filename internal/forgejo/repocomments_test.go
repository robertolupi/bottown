package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// RepoComments pages until a page brings nothing new, whatever page size
// Forgejo serves, and even when it ignores the page: each comment is read
// once, with the issue it is on.
func TestRepoCommentsPaging(t *testing.T) {
	since := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		size, cap int
		paged     bool
		calls     int
	}{
		{"empty", 0, 50, true, 1},
		{"one short page", 7, 50, true, 2},
		{"full pages", 100, 50, true, 3},
		{"a smaller cap", 65, 30, true, 4},
		{"page ignored", 65, 1000, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if r.URL.Path != "/api/v1/repos/o/r/issues/comments" || q.Get("limit") != "50" || q.Get("since") != "2026-11-01T10:00:00Z" {
					t.Errorf("request: %s?%s", r.URL.Path, r.URL.RawQuery)
				}
				page, _ := strconv.Atoi(q.Get("page"))
				start := (page - 1) * tc.cap
				if !tc.paged {
					start = 0
				}
				items := []map[string]any{}
				for i := start; i < min(start+tc.cap, tc.size); i++ {
					it := map[string]any{"id": i + 1, "body": "b", "issue_url": fmt.Sprintf("http://f/o/r/issues/%d", i+2), "pull_request_url": ""}
					if i%2 == 1 {
						it["issue_url"], it["pull_request_url"] = "", fmt.Sprintf("http://f/o/r/pulls/%d", i+2)
					}
					items = append(items, it)
				}
				_ = json.NewEncoder(w).Encode(items)
			}))
			defer srv.Close()
			c := New(context.Background(), srv.URL, "")
			got, err := c.RepoComments("o", "r", since)
			if err != nil || len(got) != tc.size || calls != tc.calls {
				t.Fatalf("comments=%d calls=%d err=%v", len(got), calls, err)
			}
			for i, cm := range got {
				want := int64(i + 2)
				if i%2 == 1 {
					want = 0
				}
				if cm.ID != int64(i+1) || cm.Issue() != want {
					t.Fatalf("comment %d: id %d, issue %d, want %d", i, cm.ID, cm.Issue(), want)
				}
			}
		})
	}
}

// A zero since asks for every comment; a failed page fails the list.
func TestRepoCommentsAllAndError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Has("since") {
			t.Errorf("since sent: %s", r.URL.RawQuery)
		}
		if calls == 2 {
			http.Error(w, "boom", 500)
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"issue_url":"http://f/o/r/issues/3"}]`))
	}))
	defer srv.Close()
	if got, err := New(context.Background(), srv.URL, "").RepoComments("o", "r", time.Time{}); err == nil || got != nil {
		t.Fatalf("got %+v, %v; want the second page's error", got, err)
	}
}

func TestRepoCommentIssue(t *testing.T) {
	for u, want := range map[string]int64{
		"http://127.0.0.1:3030/bottown/bottown/issues/436": 436,
		"http://h/sub/o/r/issues/7/":                       7,
		"":                                                 0,
		"http://h/o/r/issues/x":                            0,
		"http://h/o/r/issues/7/comments":                   0,
		"http://h/o/r/pulls/7":                             0,
	} {
		if got := (RepoComment{IssueURL: u}).Issue(); got != want {
			t.Errorf("%q: issue %d, want %d", u, got, want)
		}
	}
	if got := (RepoComment{IssueURL: "http://h/o/r/issues/7", PullRequestURL: "http://h/o/r/pulls/7"}).Issue(); got != 0 {
		t.Errorf("a pull request's comment: issue %d", got)
	}
}
