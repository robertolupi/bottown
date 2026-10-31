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
	"syscall"
	"time"
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
	Actor   string    `json:"actor"`
	Label   string    `json:"label,omitempty"`
	RunID   string    `json:"run_id,omitempty"`
	Created time.Time `json:"created"`
}

// Run is the server's record of one agent run: who, in which role, on what.
// It is operational state, not tracker state: losing it only invalidates
// in-flight run keys.
type Run struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"` // implement | verify
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
	Interactive bool       `json:"interactive,omitempty"`
	Created     time.Time  `json:"created"`
	Expires     time.Time  `json:"expires"`
	Ended       *time.Time `json:"ended,omitempty"`
	Status      string     `json:"status,omitempty"` // set when ended: succeeded | failed | revoked
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

// NewAPIKey creates and stores a new API key for actor and returns it. The
// plain key is never stored; only its hash.
func (s *Store) NewAPIKey(actor, label string) (string, error) {
	key := KeyPrefix + RandomString(32)
	err := s.Update(func(st *State) error {
		if st.Keys == nil {
			st.Keys = map[string]APIKey{}
		}
		st.Keys[HashKey(key)] = APIKey{Actor: actor, Label: label, Created: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// NewRun stores run and mints its key in one update.
func (s *Store) NewRun(run Run) (string, error) {
	key := KeyPrefix + RandomString(32)
	err := s.Update(func(st *State) error {
		if st.Keys == nil {
			st.Keys = map[string]APIKey{}
		}
		if st.Runs == nil {
			st.Runs = map[string]Run{}
		}
		if _, dup := st.Runs[run.ID]; dup {
			return fmt.Errorf("run %s exists", run.ID)
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

// EndRun marks a run ended with status and deletes its key. It reports
// false if the run does not exist.
func (s *Store) EndRun(id, status string, at time.Time) (Run, bool, error) {
	var run Run
	var ok bool
	err := s.Update(func(st *State) error {
		run, ok = st.Runs[id]
		if !ok {
			return nil
		}
		if run.Ended == nil {
			run.Ended, run.Status = &at, status
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

// Principal is who an API key authenticates: an actor, and for run keys
// the run that scopes it.
type Principal struct {
	Actor string
	Run   *Run
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
		return Principal{Actor: k.Actor}, true, nil
	}
	run, ok := st.Runs[k.RunID]
	if !ok || !run.Active(now) {
		return Principal{}, false, nil
	}
	return Principal{Actor: k.Actor, Run: &run}, true, nil
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
