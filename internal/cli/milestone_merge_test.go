package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/server"
)

func TestMilestoneMerge(t *testing.T) {
	var got []server.MilestoneMergeIn
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/forge/prs":
			fmt.Fprint(w, `[{"number":3,"state":"closed","merged":true,"head_branch":"issue/7","head_sha":"old"},
				{"number":4,"state":"open","head_branch":"issue/8","head_sha":"other"},
				{"number":5,"state":"open","head_branch":"issue/7","head_sha":"cur"},
				{"number":6,"state":"closed","head_branch":"issue/9","head_sha":"x"}]`)
		case "POST /v1/forge/pr/merge":
			var in server.MilestoneMergeIn
			_ = json.NewDecoder(r.Body).Decode(&in)
			got = append(got, in)
			_ = json.NewEncoder(w).Encode(server.MilestoneMergeOut{Number: in.Number, HeadSHA: in.HeadSHA})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := client.New(srv.URL, "k")
	for _, tc := range []struct {
		issue      int64
		sha        string
		wantNumber int64
		wantSHA    string
	}{
		{7, "", 5, "cur"},        // the open PR, at its head
		{7, "given", 5, "given"}, // --sha passes through
		{8, "", 4, "other"},
	} {
		got = nil
		if _, err := milestoneMerge(t.Context(), c, "demo", tc.issue, tc.sha); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Repo != "demo" || got[0].Number != tc.wantNumber || got[0].HeadSHA != tc.wantSHA {
			t.Errorf("issue %d --sha %q: requests %+v, want !%d at %s", tc.issue, tc.sha, got, tc.wantNumber, tc.wantSHA)
		}
	}
	got = nil
	if _, err := milestoneMerge(t.Context(), c, "demo", 9, ""); err == nil || len(got) != 0 {
		t.Errorf("issue 9 (closed, unmerged PR): err = %v, requests %+v; want an error and no merge", err, got)
	}
}
