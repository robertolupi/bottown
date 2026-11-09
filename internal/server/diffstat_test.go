package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// A merged PR's diffstat comes from git, from its merge base to its head,
// without downloading its diff; without the repositories, or without the
// PR's commits in them, it is parsed from Forgejo's diff.
func TestPRDiffStat(t *testing.T) {
	repo := newPushRepo(t)
	for _, tc := range []struct {
		name     string
		gitRoot  string
		head     string
		want     []DiffStatFile
		wantDiff bool
	}{
		{"git", repo.root, repo.new, []DiffStatFile{{Path: "agent.txt", Added: 2}}, false},
		{"no repositories", "", repo.new, []DiffStatFile{{Path: "forge.txt", Added: 1, Deleted: 1}}, true},
		{"head not in git", repo.root, strings.Repeat("e", 40), []DiffStatFile{{Path: "forge.txt", Added: 1, Deleted: 1}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diffs atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, ".diff") {
					diffs.Add(1)
					_, _ = w.Write([]byte("diff --git a/forge.txt b/forge.txt\n--- a/forge.txt\n+++ b/forge.txt\n@@ -1 +1 @@\n-a\n+b\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "state": "closed", "merged": true, "user": map[string]string{"login": "claude"},
					"merge_base": repo.base, "merge_commit_sha": repo.main,
					"head": map[string]string{"ref": "issue/1", "sha": tc.head}, "base": map[string]string{"ref": "main"}})
			}))
			defer upstream.Close()
			store, err := secrets.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(func(st *secrets.State) error {
				st.Tokens = map[string]secrets.ActorToken{"rlupi": {Token: "tok"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: tc.gitRoot, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
			out, err := s.opPRDiffStat(context.Background(), secrets.Principal{Actor: "rlupi"}, Target{Repo: "demo", Number: 12})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out.Files, tc.want) {
				t.Errorf("files = %+v, want %+v", out.Files, tc.want)
			}
			if got := diffs.Load() > 0; got != tc.wantDiff {
				t.Errorf("downloaded the diff: %v, want %v", got, tc.wantDiff)
			}
		})
	}
}

func TestParseNumstat(t *testing.T) {
	out := "3\t1\ta.go\x00-\t-\timg.png\x00" + "2\t0\t\x00old/name.go\x00new/name.go\x00" + "0\t0\twith\ttab.txt\x00"
	got, err := parseNumstat(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []DiffStatFile{
		{Path: "a.go", Added: 3, Deleted: 1},
		{Path: "img.png", Binary: true},
		{Path: "new/name.go", Added: 2},
		{Path: "with\ttab.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if files, err := parseNumstat(""); err != nil || len(files) != 0 {
		t.Errorf("empty: %+v, %v", files, err)
	}
	for _, bad := range []string{"x\x00", "a\tb\tc\x00", "1\t1\t\x00old\x00"} {
		if _, err := parseNumstat(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
