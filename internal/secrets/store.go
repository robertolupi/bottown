// Package secrets holds the credentials bottown keeps inside its container:
// the Forgejo admin token, one Forgejo token per actor, the owner's pending
// initial password, and the bottown API keys handed to host-side clients.
//
// Everything lives in one JSON file under the data directory. Each operation
// takes an advisory lock and re-reads the file, so the long-running server and
// short-lived `bottown admin` processes can share it safely.
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// KeyPrefix marks bottown API keys so they are recognisable in logs and greps.
const KeyPrefix = "btk_"

// ActorToken is a Forgejo access token minted for one actor.
type ActorToken struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	Token  string   `json:"token"`
}

// APIKey is the stored form of a bottown API key; only its hash is kept.
// A run key also names its run, which bounds what the key may do and for
// how long.
type APIKey struct {
	Actor string `json:"actor"`
	Label string `json:"label,omitempty"`
	RunID string `json:"run_id,omitempty"`
	// Scope is ScopeRead for keys that may only read and start runs;
	// empty means full access for the actor.
	Scope   string    `json:"scope,omitempty"`
	Created time.Time `json:"created"`
}

// Run is the server's record of one agent run: who, in which role, on what,
// and once it ends, its digest. It is operational state, not tracker state:
// losing it invalidates in-flight run keys and loses run history, nothing
// the forge holds.
type Run struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"` // implement | verify | discuss
	Actor           string `json:"actor"`
	Family          string `json:"family"`
	Harness         string `json:"harness,omitempty"` // which CLI runs the actor
	Org             string `json:"org"`
	Repo            string `json:"repo"`
	Issue           int64  `json:"issue,omitempty"`
	PR              int64  `json:"pr,omitempty"`
	Branch          string `json:"branch,omitempty"`
	SHA             string `json:"sha,omitempty"`    // verify: PR head at creation
	Author          string `json:"author,omitempty"` // verify: PR author
	AllowSameFamily bool   `json:"allow_same_family,omitempty"`
	// Interactive runs are steered by the owner in the harness's TUI, so
	// their result is partly the owner's work.
	Interactive bool `json:"interactive,omitempty"`
	// StartedBy is the owner's login, or the actor's for self-started runs.
	StartedBy string     `json:"started_by,omitempty"`
	Created   time.Time  `json:"created"`
	Expires   time.Time  `json:"expires"`
	Ended     *time.Time `json:"ended,omitempty"`
	Status    string     `json:"status,omitempty"` // set when ended: succeeded | failed | revoked
	// Digest is what the run's ender reported: set once, when it ends.
	Digest
	// Outcome is the work result the server saw the run produce through
	// its tools, kept apart from Status (whether the run executed): one of
	// the Outcome constants, or empty when none was recorded.
	Outcome string `json:"outcome,omitempty"`
	// Purged runs were dropped by PurgeRuns: only their digest is kept.
	Purged bool `json:"purged,omitempty"`
}

// Digest is what is known of a run's outcome once it ends: the agent's
// final message or handover summary, and the usage its harness reported.
type Digest struct {
	Summary string `json:"summary,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
}

// Run outcomes. A verify run's is its latest review verdict; an implement
// or plan run's is the pull request it opened.
const (
	OutcomeApproved         = "approved"
	OutcomeChangesRequested = "changes_requested"
	OutcomeCommented        = "commented"
	OutcomePROpened         = "pr_opened"
)

// MaxSummary bounds a stored digest summary, in bytes.
const MaxSummary = 4000

// Usage is what a harness reported a run used. A field is nil when the
// harness does not report it: unknown stays unknown, never estimated.
type Usage struct {
	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"` // thinking included
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	// CostUSD is the harness's own figure, at list price.
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	Turns      *int64   `json:"turns,omitempty"`
	DurationMS *int64   `json:"duration_ms,omitempty"` // as the harness measured it
}

// Active reports whether the run's key is usable at t.
func (r Run) Active(t time.Time) bool { return r.Ended == nil && t.Before(r.Expires) }

// State is the full content of the secrets file.
type State struct {
	AdminToken    string                `json:"admin_token,omitempty"`
	Tokens        map[string]ActorToken `json:"tokens,omitempty"`
	OwnerPassword string                `json:"owner_password,omitempty"`
	Keys          map[string]APIKey     `json:"keys,omitempty"` // sha256(key) hex -> key
	Runs          map[string]Run        `json:"runs,omitempty"` // run id -> run
	// History holds the digests of purged runs, by id.
	History map[string]Run `json:"history,omitempty"`
}

// Store is a lock-protected JSON file of State.
type Store struct {
	path string
}

// Open returns the store at dir/secrets.json, creating dir with 0700.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "secrets.json")}, nil
}

// Path is the location of the secrets file.
func (s *Store) Path() string { return s.path }

// Read returns a snapshot of the current state.
func (s *Store) Read() (State, error) {
	var st State
	err := s.locked(syscall.LOCK_SH, func() error {
		var err error
		st, err = s.load()
		return err
	})
	return st, err
}

// Update applies fn to the current state under an exclusive lock and writes
// the result atomically. If fn returns an error nothing is written.
func (s *Store) Update(fn func(*State) error) error {
	return s.locked(syscall.LOCK_EX, func() error {
		st, err := s.load()
		if err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		return s.save(st)
	})
}

// ScopeRead marks a key that may read and start runs, but not write.
const ScopeRead = "read"

// NewAPIKey creates and stores a new API key for actor with scope ("" for
// full, or ScopeRead) and returns it. The plain key is never stored; only
// its hash.
func (s *Store) NewAPIKey(actor, label, scope string) (string, error) {
	key := KeyPrefix + RandomString(32)
	err := s.Update(func(st *State) error {
		if st.Keys == nil {
			st.Keys = map[string]APIKey{}
		}
		st.Keys[HashKey(key)] = APIKey{Actor: actor, Label: label, Scope: scope, Created: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// RunLimitError reports that an actor is at its limit on live batch runs.
type RunLimitError struct {
	Actor string
	Limit int
	Runs  []string
}

func (e *RunLimitError) Error() string {
	return fmt.Sprintf("actor %s is at its limit of %d live batch runs: %s", e.Actor, e.Limit, strings.Join(e.Runs, ", "))
}

// NewRun stores run and mints its key in one update. The batch run limit is
// checked in the same update; zero means no limit, and interactive runs are
// neither limited nor counted.
func (s *Store) NewRun(run Run, batchRunLimit int) (string, error) {
	key := KeyPrefix + RandomString(32)
	err := s.Update(func(st *State) error {
		if st.Keys == nil {
			st.Keys = map[string]APIKey{}
		}
		if st.Runs == nil {
			st.Runs = map[string]Run{}
		}
		if _, dup := st.Runs[run.ID]; dup || st.History[run.ID].ID != "" {
			return fmt.Errorf("run %s exists", run.ID)
		}
		if !run.Interactive && batchRunLimit > 0 {
			now := time.Now().UTC()
			var live []string
			for _, existing := range st.Runs {
				if existing.Actor == run.Actor && !existing.Interactive && existing.Active(now) {
					live = append(live, existing.ID)
				}
			}
			if len(live) >= batchRunLimit {
				slices.Sort(live)
				return &RunLimitError{Actor: run.Actor, Limit: batchRunLimit, Runs: live}
			}
		}
		st.Runs[run.ID] = run
		st.Keys[HashKey(key)] = APIKey{Actor: run.Actor, Label: "run " + run.ID, RunID: run.ID, Created: run.Created}
		return nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// EndRun marks a run ended with status and digest, and deletes its key. A
// run that has already ended keeps its status and digest. It reports false
// if the run does not exist.
func (s *Store) EndRun(id, status string, at time.Time, d Digest) (Run, bool, error) {
	var run Run
	var ok bool
	err := s.Update(func(st *State) error {
		run, ok = st.Runs[id]
		if !ok {
			return nil
		}
		if run.Ended == nil {
			d.Summary = truncateUTF8(d.Summary, MaxSummary)
			run.Ended, run.Status, run.Digest = &at, status, d
			st.Runs[id] = run
		}
		for h, k := range st.Keys {
			if k.RunID == id {
				delete(st.Keys, h)
			}
		}
		return nil
	})
	return run, ok, err
}

// SetOutcome records the outcome of a run that has not ended; a later call
// replaces it (a verifier's newer review supersedes the older). It reports
// false if the run does not exist or has ended.
func (s *Store) SetOutcome(id, outcome string) (bool, error) {
	var ok bool
	err := s.Update(func(st *State) error {
		run, found := st.Runs[id]
		if !found || run.Ended != nil {
			return nil
		}
		run.Outcome, ok = outcome, true
		st.Runs[id] = run
		return nil
	})
	return ok, err
}

// PurgeRuns drops the ended runs for which match is true, with any keys
// left for them, and returns their ids. Each one's digest moves to History,
// so what happened outlives the run. Runs that have not ended (live, or
// expired without ending) are never purged.
func (s *Store) PurgeRuns(match func(Run) bool) ([]string, error) {
	var ids []string
	err := s.Update(func(st *State) error {
		for id, run := range st.Runs {
			if run.Ended == nil || !match(run) {
				continue
			}
			if st.History == nil {
				st.History = map[string]Run{}
			}
			run.Purged = true
			st.History[id] = run
			delete(st.Runs, id)
			ids = append(ids, id)
		}
		for h, k := range st.Keys {
			if k.RunID != "" && slices.Contains(ids, k.RunID) {
				delete(st.Keys, h)
			}
		}
		return nil
	})
	slices.Sort(ids)
	return ids, err
}

// Principal is who an API key authenticates: an actor, and for run keys
// the run that scopes it.
type Principal struct {
	Actor string
	Run   *Run
	// ReadOnly principals (read-scoped keys outside a run) may only read and
	// start runs.
	ReadOnly bool
}

// Lookup resolves key. Run keys whose run has ended or expired do not
// resolve.
func (s *Store) Lookup(key string, now time.Time) (Principal, bool, error) {
	st, err := s.Read()
	if err != nil {
		return Principal{}, false, err
	}
	k, ok := st.Keys[HashKey(key)]
	if !ok {
		return Principal{}, false, nil
	}
	if k.RunID == "" {
		return Principal{Actor: k.Actor, ReadOnly: k.Scope == ScopeRead}, true, nil
	}
	run, ok := st.Runs[k.RunID]
	if !ok || !run.Active(now) {
		return Principal{}, false, nil
	}
	return Principal{Actor: k.Actor, Run: &run}, true, nil
}

// truncateUTF8 cuts s to at most n bytes, on a rune boundary, marking the
// cut with an ellipsis.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n-len("…")]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// HashKey is the storage form of an API key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// RandomString returns n random bytes, URL-safe base64 encoded.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Store) locked(how int, fn func() error) error {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		return fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *Store) load() (State, error) {
	var st State
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return st, nil
}

func (s *Store) save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
