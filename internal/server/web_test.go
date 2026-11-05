package server

import (
	"testing"
	"time"
)

// Adding a UI session sweeps out expired ones, even ones never checked again.
func TestNewUISessionSweepsExpired(t *testing.T) {
	var w webState
	stale, _ := w.newUISession()
	w.uiSessions[stale] = time.Now().Add(-time.Minute)
	live, _ := w.newUISession()
	if _, ok := w.uiSessions[stale]; ok {
		t.Error("expired session kept after another was added")
	}
	if !w.validUISession(live) {
		t.Error("new session not valid")
	}
}
