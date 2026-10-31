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
type APIKey struct {
	Actor   string    `json:"actor"`
	Label   string    `json:"label,omitempty"`
	Created time.Time `json:"created"`
}

// State is the full content of the secrets file.
type State struct {
	AdminToken    string                `json:"admin_token,omitempty"`
	Tokens        map[string]ActorToken `json:"tokens,omitempty"`
	OwnerPassword string                `json:"owner_password,omitempty"`
	Keys          map[string]APIKey     `json:"keys,omitempty"` // sha256(key) hex -> key
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

// LookupKey returns the actor owning key, if any.
func (s *Store) LookupKey(key string) (string, bool, error) {
	st, err := s.Read()
	if err != nil {
		return "", false, err
	}
	k, ok := st.Keys[HashKey(key)]
	return k.Actor, ok, nil
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
