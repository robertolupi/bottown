package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestEndRunPurge(t *testing.T) {
	dir := t.TempDir()
	store, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, err = store.NewRun(secrets.Run{ID: "r-test", Kind: "implement", Actor: "claude", Created: now, Expires: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Owner: "owner",
		Store: store,
		Log:   slog.Default(),
	}

	// Non-owner cannot purge
	req := httptest.NewRequest("DELETE", "/v1/runs/r-test?purge=true", nil)
	req.SetPathValue("id", "r-test")
	ctx := context.WithValue(context.Background(), ctxKey{}, secrets.Principal{Actor: "claude"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	s.endRun(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-owner purge: got %d, want 403", w.Code)
	}

	// Owner can purge
	w = httptest.NewRecorder()
	req = req.WithContext(context.WithValue(context.Background(), ctxKey{}, secrets.Principal{Actor: "owner"}))
	s.endRun(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("owner purge: got %d, want 200", w.Code)
	}

	// Purge non-existent returns 404
	w = httptest.NewRecorder()
	s.endRun(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("repeat purge: got %d, want 404", w.Code)
	}
}
