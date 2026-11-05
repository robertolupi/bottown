package hostctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Secrets stores the owner's host-side credentials for one instance: the
// Forgejo web password and the bottown API key.
type Secrets interface {
	Get(account string) (string, bool, error)
	Set(account, value string) error
	Delete(account string) error
}

// Account names used in the secret store.
const (
	AccountPassword = "forgejo-password"
	AccountAPIKey   = "api-key"
)

// OpenSecrets returns the macOS Keychain on darwin, or a 0600 JSON file in
// dir elsewhere or when $BOTTOWN_SECRET_STORE=file.
func OpenSecrets(instance, dir string) Secrets {
	if runtime.GOOS == "darwin" && os.Getenv("BOTTOWN_SECRET_STORE") != "file" {
		return Keychain{Service: "bottown-" + instance}
	}
	return FileSecrets{Path: filepath.Join(dir, "host-secrets.json")}
}

// Keychain stores generic passwords via /usr/bin/security.
type Keychain struct {
	Service string
	Bin     string        // defaults to "security"
	Timeout time.Duration // per command; defaults to KeychainTimeout
}

// KeychainTimeout bounds one security command. A locked keychain, or a
// binary whose code signature changed (after `make install`), can make it
// wait on a modal prompt; `bottown credential` sits in git's credential
// path, so without a bound a run, a flow or a background `bottown web`
// would hang on a dialog nobody sees.
const KeychainTimeout = 5 * time.Second

// errSecItemNotFound is security's exit status for a missing item.
const errSecItemNotFound = 44

// security runs the security command on the item account and returns its
// stdout. A failure names the item; a missing item is errKeychainNotFound.
func (k Keychain) security(op, account string, args ...string) (string, error) {
	bin, timeout := k.Bin, k.Timeout
	if bin == "" {
		bin = "security"
	}
	if timeout <= 0 {
		timeout = KeychainTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{op, "-s", k.Service, "-a", account}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = time.Second // a stray child must not hold the pipes open
	err := cmd.Run()
	item := fmt.Sprintf("keychain item %s/%s", k.Service, account)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), nil
	case ctx.Err() != nil:
		return "", fmt.Errorf("%s: security %s gave no answer in %s (is the keychain locked, or waiting on a prompt?)", item, op, timeout)
	case errors.As(err, &ee) && ee.ExitCode() == errSecItemNotFound:
		return "", errKeychainNotFound
	default:
		return "", fmt.Errorf("%s: security %s: %w: %s", item, op, err, strings.TrimSpace(errb.String()))
	}
}

var errKeychainNotFound = errors.New("keychain item not found")

func (k Keychain) Get(account string) (string, bool, error) {
	out, err := k.security("find-generic-password", account, "-w")
	if errors.Is(err, errKeychainNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(out, "\n"), true, nil
}

func (k Keychain) Set(account, value string) error {
	_, err := k.security("add-generic-password", account, "-U", "-l", k.Service+" "+account, "-w", value)
	return err
}

func (k Keychain) Delete(account string) error {
	_, err := k.security("delete-generic-password", account)
	if errors.Is(err, errKeychainNotFound) {
		return nil
	}
	return err
}

// FileSecrets is the fallback store.
type FileSecrets struct{ Path string }

func (f FileSecrets) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (f FileSecrets) save(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.Path, b, 0o600)
}

func (f FileSecrets) Get(account string) (string, bool, error) {
	m, err := f.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[account]
	return v, ok, nil
}

func (f FileSecrets) Set(account, value string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	m[account] = value
	return f.save(m)
}

func (f FileSecrets) Delete(account string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, account)
	return f.save(m)
}
