package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

// Audit retention: entries older than auditKeep, or beyond the newest
// auditMax, are dropped.
const (
	auditKeep = 14 * 24 * time.Hour
	auditMax  = 20000
	// auditSlack is how many dropped lines the file may hold before it is
	// rewritten from memory.
	auditSlack = 1000
)

// AuditEntry is one policy decision the server made: a forge write it let
// through, or an operation it refused.
type AuditEntry struct {
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`
	Run      string    `json:"run,omitempty"`
	Repo     string    `json:"repo,omitempty"` // org/name
	Op       string    `json:"op"`             // api | git | an operation, e.g. pr_create
	Method   string    `json:"method,omitempty"`
	Path     string    `json:"path,omitempty"`
	Decision string    `json:"decision"` // allow | deny | reject
	Rule     string    `json:"rule,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Status   int       `json:"status,omitempty"`
}

// AuditFilter selects entries; empty fields match everything.
type AuditFilter struct {
	Repo     string
	Actor    string
	Run      string
	Decision string // one decision, or several separated by commas
	Since    time.Time
	Limit    int // the newest Limit entries; 0 means all
}

func (f AuditFilter) match(e AuditEntry) bool {
	return (f.Repo == "" || strings.EqualFold(e.Repo, f.Repo)) &&
		(f.Actor == "" || e.Actor == f.Actor) &&
		(f.Run == "" || e.Run == f.Run) &&
		(f.Decision == "" || slices.Contains(strings.Split(f.Decision, ","), e.Decision)) &&
		(f.Since.IsZero() || !e.Time.Before(f.Since))
}

// AuditLog keeps the server's policy decisions for a bounded time, in
// memory and in a JSON-lines file, so they survive restarts and upgrades.
type AuditLog struct {
	path string
	now  func() time.Time

	mu        sync.Mutex
	entries   []AuditEntry // oldest first
	fileLines int
}

// OpenAudit loads dir/audit.jsonl, dropping expired entries.
func OpenAudit(dir string) (*AuditLog, error) {
	a := &AuditLog{path: filepath.Join(dir, "audit.jsonl"), now: time.Now}
	f, err := os.Open(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		a.fileLines++
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			a.entries = append(a.entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	return a, a.compactIfNeeded()
}

// Record stores e, stamping its time if unset.
func (a *AuditLog) Record(e AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = a.now().UTC()
	}
	a.entries = append(a.entries, e)
	a.prune()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if err := f.Close(); werr == nil {
		werr = err
	}
	if werr != nil {
		return werr
	}
	a.fileLines++
	return a.compactIfNeeded()
}

// Query returns the entries matching f, oldest first.
func (a *AuditLog) Query(f AuditFilter) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	out := []AuditEntry{}
	for _, e := range a.entries {
		if f.match(e) {
			out = append(out, e)
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}

// prune drops expired entries and those beyond auditMax; callers hold mu.
func (a *AuditLog) prune() {
	cut := a.now().Add(-auditKeep)
	i := 0
	for i < len(a.entries) && (a.entries[i].Time.Before(cut) || len(a.entries)-i > auditMax) {
		i++
	}
	if i > 0 {
		a.entries = append([]AuditEntry(nil), a.entries[i:]...)
	}
}

// compactIfNeeded rewrites the file from memory once it holds enough
// dropped lines; callers hold mu.
func (a *AuditLog) compactIfNeeded() error {
	if a.fileLines <= len(a.entries)+auditSlack {
		return nil
	}
	tmp := a.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range a.entries {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.fileLines = len(a.entries)
	return nil
}

// record logs a decision and, when the server keeps an audit log, stores
// it. A failure to store is logged, never fatal to the request.
func (s *Server) record(e AuditEntry) {
	s.Log.Info("audit", "actor", e.Actor, "run", e.Run, "repo", e.Repo, "op", e.Op, "method", e.Method,
		"path", e.Path, "decision", e.Decision, "rule", e.Rule, "detail", e.Detail, "status", e.Status)
	if s.Audit == nil {
		return
	}
	if err := s.Audit.Record(e); err != nil {
		s.Log.Error("store audit entry", "err", err)
	}
}

func runID(p secrets.Principal) string {
	if p.Run != nil {
		return p.Run.ID
	}
	return ""
}

func runRepo(p secrets.Principal) string {
	if p.Run != nil {
		return p.Run.Org + "/" + p.Run.Repo
	}
	return ""
}

// apiRepo is the org/name an API path addresses, if any.
func apiRepo(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		return parts[1] + "/" + parts[2]
	}
	return ""
}

// gitRepo is the org/name a git smart-HTTP path addresses.
func gitRepo(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	}
	return ""
}

// listAudit is GET /v1/audit: the stored decisions, oldest first,
// filtered by repo (org/name, or a name in the instance's org), actor,
// run, decision (one or a comma-separated list), since (RFC 3339) and limit (the newest N).
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := AuditFilter{Repo: q.Get("repo"), Actor: q.Get("actor"), Run: q.Get("run"), Decision: q.Get("decision")}
	if f.Repo != "" && !strings.Contains(f.Repo, "/") {
		f.Repo = s.desired().Org + "/" + f.Repo
	}
	if f.Decision != "" {
		for _, d := range strings.Split(f.Decision, ",") {
			switch d {
			case "allow", "deny", "reject":
			default:
				writeError(w, http.StatusBadRequest, "decision must be allow, deny or reject, or a comma-separated list of them")
				return
			}
		}
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC 3339")
			return
		}
		f.Since = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		f.Limit = n
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusOK, []AuditEntry{})
		return
	}
	writeJSON(w, http.StatusOK, s.Audit.Query(f))
}
