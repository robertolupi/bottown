package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
)

// TestCarryOver runs pr show on each head move of the shared patch cases.
// agy's approval at the old head was dismissed by Forgejo for the new
// commits, kimi's by the owner. When the patch is unchanged agy's
// approval carries over and is undismissed, so Forgejo counts it too;
// kimi's dismissal stands. Otherwise nothing changes.
func TestCarryOver(t *testing.T) {
	for _, tc := range reviewtest.PatchCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "bottown"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.GitDir, filepath.Join(root, "bottown", "demo.git")); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var undismissed []string
			dismissed := map[int64]bool{1: true, 2: true}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				p := r.URL.Path
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(p, "/undismissals"):
					if r.Header.Get("Authorization") != "token tok_owner" {
						http.Error(w, "Must be repo admin", http.StatusForbidden)
						return
					}
					parts := strings.Split(p, "/")
					id := parts[len(parts)-2]
					n, _ := strconv.ParseInt(id, 10, 64)
					undismissed = append(undismissed, id)
					dismissed[n] = false
					_, _ = w.Write([]byte("{}"))
				case strings.HasSuffix(p, "/timeline"):
					_ = json.NewEncoder(w).Encode([]map[string]any{
						{"id": 10, "type": "pull_push"},
						{"id": 11, "type": "dismiss_review", "review_id": 1, "body": forgejo.AutoDismissMessage},
						{"id": 12, "type": "dismiss_review", "review_id": 2, "body": "not this one"},
					})
				case strings.HasSuffix(p, "/reviews"):
					if r.URL.Query().Get("page") != "1" {
						_, _ = w.Write([]byte("[]"))
						return
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{
						{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"}, "commit_id": tc.Old, "official": true, "stale": true, "dismissed": dismissed[1]},
						{"id": 2, "state": "APPROVED", "user": map[string]string{"login": "kimi"}, "commit_id": tc.Old, "official": true, "stale": true, "dismissed": dismissed[2]},
					})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "state": "open", "user": map[string]string{"login": "claude"},
						"head": map[string]string{"ref": "issue/1", "sha": tc.Head}, "base": map[string]string{"ref": "main"}})
				}
			}))
			defer upstream.Close()
			store, err := secrets.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(func(st *secrets.State) error {
				st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "rlupi": {Token: "tok_owner"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: root, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown",
				Actors: []config.Actor{{Name: "claude", Family: "anthropic"}, {Name: "agy", Family: "google"}, {Name: "kimi", Family: "moonshot"}}}}}
			pr, err := s.opPR(context.Background(), secrets.Principal{Actor: "claude"}, Target{Repo: "demo", Number: 12}, false)
			if err != nil {
				t.Fatal(err)
			}
			a := pr.Assessment
			if !tc.Same {
				if a.Decision != review.NeedsReview || len(undismissed) != 0 {
					t.Fatalf("assessment = %+v, undismissed %v; want needs review and none", a, undismissed)
				}
				return
			}
			if a.Decision != review.Approved || a.Review.ID != 1 || a.Carried == nil || a.Carried.From != tc.Old || !strings.Contains(a.Reason, "carried over") {
				t.Fatalf("assessment = %+v, want agy's approval carried over", a)
			}
			if !slices.Equal(undismissed, []string{"1"}) {
				t.Fatalf("undismissed %v, want only Forgejo's own dismissal", undismissed)
			}
			if pr.Reviews[0].Dismissed || !pr.Reviews[1].Dismissed {
				t.Fatalf("reviews = %+v", pr.Reviews)
			}
		})
	}
}
