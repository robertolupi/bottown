package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

func TestCompleteLists(t *testing.T) {
	for _, kind := range []string{"issues", "pulls", "reviews"} {
		for _, size := range []int{0, 50, 101} {
			for _, fail := range []bool{false, true} {
				if fail && size == 0 {
					continue
				}
				t.Run(fmt.Sprintf("%s/%d/error=%v", kind, size, fail), func(t *testing.T) {
					calls := 0
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						page, _ := strconv.Atoi(r.URL.Query().Get("page"))
						if page != calls || r.URL.Query().Get("limit") != "50" {
							t.Errorf("query: %s", r.URL.RawQuery)
						}
						if fail && page == 2 {
							http.Error(w, "later page failed", 500)
							return
						}
						start := (page - 1) * 50
						end := min(start+50, size)
						items := []map[string]any{}
						for i := start; i < end; i++ {
							items = append(items, map[string]any{"id": i + 1, "number": i + 1})
						}
						_ = json.NewEncoder(w).Encode(items)
					}))
					defer srv.Close()
					c := New(context.Background(), srv.URL, "")
					var n int
					var err error
					switch kind {
					case "issues":
						xs, _, e := c.AllIssues("o", "r", sdk.ListIssueOption{State: sdk.StateOpen, Type: sdk.IssueTypeIssue})
						n, err = len(xs), e
					case "pulls":
						xs, _, e := c.AllPullRequests("o", "r", sdk.ListPullRequestsOptions{State: sdk.StateOpen})
						n, err = len(xs), e
					case "reviews":
						xs, _, e := c.AllReviews("o", "r", 1, sdk.ListPullReviewsOptions{})
						n, err = len(xs), e
					}
					if fail {
						if err == nil || n != 0 || calls != 2 {
							t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
						}
					} else if err != nil || n != size || calls != size/50+1 {
						t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
					}
				})
			}
		}
	}
}

// Forgejo ignores page/limit for issue comments, including long threads.
func TestAllCommentsUnpaginated(t *testing.T) {
	for _, size := range []int{0, 50, 101} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 {
					http.Error(w, "unexpected repeated request", 500)
					return
				}
				if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("limit") != "0" {
					t.Errorf("pagination parameters: %s", r.URL.RawQuery)
				}
				items := make([]*sdk.Comment, size)
				for i := range items {
					items[i] = &sdk.Comment{ID: int64(i + 1)}
				}
				_ = json.NewEncoder(w).Encode(items)
			}))
			defer srv.Close()
			c := New(context.Background(), srv.URL, "")
			items, _, err := c.AllComments("o", "r", 1, sdk.ListIssueCommentOptions{ListOptions: sdk.ListOptions{Page: 2, PageSize: 50}})
			if err != nil || len(items) != size || calls != 1 {
				t.Fatalf("items=%d calls=%d err=%v", len(items), calls, err)
			}
		})
	}
}
