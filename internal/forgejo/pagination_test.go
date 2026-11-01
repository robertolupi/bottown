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
	for _, kind := range []string{"issues", "pulls", "comments", "reviews"} {
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
					case "comments":
						xs, _, e := c.AllComments("o", "r", 1, sdk.ListIssueCommentOptions{})
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
