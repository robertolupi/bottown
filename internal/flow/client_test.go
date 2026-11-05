package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/server"
)

func TestOpenPRBeyondFirstForgePage(t *testing.T) {
	// The forge API supplies a complete list, including the matching PR after
	// the SDK's 50-item page boundary.
	prs := make([]server.PRItem, 51)
	for i := range prs {
		prs[i] = server.PRItem{Number: int64(i + 1), HeadBranch: "other"}
	}
	prs[50].HeadBranch = "issue/111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/forge/prs" || r.URL.Query().Get("state") != "open" {
			t.Errorf("request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(prs)
	}))
	defer srv.Close()
	c := &Client{c: client.New(srv.URL, "")}
	pr, found, err := c.OpenPR(context.Background(), "bottown/bottown", "issue/111")
	if err != nil || !found || pr.Number != 51 {
		t.Fatalf("pr=%v found=%v err=%v", pr, found, err)
	}
}
