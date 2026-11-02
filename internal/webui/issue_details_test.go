package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
)

func TestIssueDetails(t *testing.T) {
	fix := defaultRunsFixture()
	fix.issues = []*sdk.Issue{
		{
			Index:   129,
			Title:   "Show issue titles",
			Body:    "Description with **Markdown**",
			State:   sdk.StateOpen,
			HTMLURL: "http://forge/bottown/bottown/issues/129",
		},
	}
	_, port := newRunsFixtureServer(t, fix)
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "test", ServerPort: port},
		RepoTop:  t.TempDir(),
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	details, err := s.ComputeIssueDetails(context.Background(), 129)
	if err != nil {
		t.Fatal(err)
	}
	if details.Title != "Show issue titles" || details.Body != "Description with **Markdown**" || details.State != "open" {
		t.Fatalf("unexpected details: %+v", details)
	}

	req := httptest.NewRequest(http.MethodGet, s.BaseURL()+"/api/issues/129", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName, Value: s.sessionToken})
	res := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET issue details = %d: %s", res.Code, res.Body.String())
	}
	var fromAPI IssueDetails
	if err := json.NewDecoder(res.Body).Decode(&fromAPI); err != nil {
		t.Fatal(err)
	}
	if fromAPI != *details {
		t.Fatalf("API details = %+v, want %+v", fromAPI, *details)
	}
}
