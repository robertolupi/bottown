package hostctl

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
type Keychain struct{ Service string }

func (k Keychain) Get(account string) (string, bool, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", account, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimRight(string(out), "\n"), true, nil
}

func (k Keychain) Set(account, value string) error {
	out, err := exec.Command("security", "add-generic-password", "-U", "-s", k.Service, "-a", account,
		"-l", k.Service+" "+account, "-w", value).CombinedOutput()
	if err != nil {
		return errors.New("keychain: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func (k Keychain) Delete(account string) error {
	err := exec.Command("security", "delete-generic-password", "-s", k.Service, "-a", account).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
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
