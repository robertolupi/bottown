package hostctl

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSecurity writes a stand-in for /usr/bin/security that runs script.
func fakeSecurity(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the security stand-in is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestKeychainTimesOutOnAPrompt(t *testing.T) {
	k := Keychain{Service: "bottown-test", Bin: fakeSecurity(t, "exec sleep 30"), Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, _, err := k.Get(AccountAPIKey)
	if err == nil {
		t.Fatal("Get succeeded on a security that never answers")
	}
	// Well short of the script's 30s sleep, so the timeout ended it, and
	// long enough for a loaded host to start and kill a new script, which
	// macOS's security scan can delay for seconds (see TestKeychainResults).
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Get took %s; the timeout is 200ms", d)
	}
	for _, want := range []string{"bottown-test/api-key", "no answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestKeychainResults(t *testing.T) {
	// The first exec of a new script can wait on macOS's security scan for
	// seconds on a loaded host: these cases are not about the timeout.
	const timeout = time.Minute
	found := Keychain{Service: "s", Bin: fakeSecurity(t, `echo "secret"`), Timeout: timeout}
	if v, ok, err := found.Get("a"); err != nil || !ok || v != "secret" {
		t.Errorf("Get = %q, %v, %v; want secret, true, nil", v, ok, err)
	}
	missing := Keychain{Service: "s", Bin: fakeSecurity(t, "exit 44"), Timeout: timeout}
	if _, ok, err := missing.Get("a"); err != nil || ok {
		t.Errorf("Get of a missing item = %v, %v; want false, nil", ok, err)
	}
	failing := Keychain{Service: "s", Bin: fakeSecurity(t, "echo denied >&2; exit 51"), Timeout: timeout}
	if _, _, err := failing.Get("a"); err == nil || !strings.Contains(err.Error(), "s/a") || !strings.Contains(err.Error(), "denied") {
		t.Errorf("Get error = %v; want the item and security's message", err)
	}
}

// fakeLegacy is a legacy store that counts its reads.
type fakeLegacy struct {
	items map[string]string
	err   error
	reads *int
}

func (l fakeLegacy) Get(account string) (string, bool, error) {
	*l.reads++
	v, ok := l.items[account]
	return v, ok, l.err
}

func TestImportingCopiesTheLegacyItemsOnce(t *testing.T) {
	reads := 0
	path := filepath.Join(t.TempDir(), "inst", "host-secrets.json")
	legacy := fakeLegacy{items: map[string]string{AccountAPIKey: "k"}, reads: &reads}
	s := importing{file: FileSecrets{Path: path}, legacy: legacy}
	if v, ok, err := s.Get(AccountAPIKey); err != nil || !ok || v != "k" {
		t.Fatalf("Get = %q, %v, %v; want the imported key", v, ok, err)
	}
	if _, ok, err := s.Get(AccountPassword); err != nil || ok {
		t.Errorf("Get of an item the legacy store lacks = %v, %v; want false, nil", ok, err)
	}
	if reads != len(legacyAccounts) {
		t.Errorf("legacy store read %d times; want one import of %d accounts", reads, len(legacyAccounts))
	}
	if err := s.Delete(AccountAPIKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(AccountAPIKey); ok {
		t.Error("a deleted key came back from the legacy store")
	}
	if err := s.Set(AccountPassword, "p"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := (FileSecrets{Path: path}).Get(AccountPassword); v != "p" {
		t.Errorf("file holds password %q; want p", v)
	}
	if reads != len(legacyAccounts) {
		t.Errorf("legacy store read %d times after the import; want no more reads", reads)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("secret file: %v, %v; want mode 0600", fi, err)
	}
}

func TestImportingRetriesAFailedImport(t *testing.T) {
	reads := 0
	path := filepath.Join(t.TempDir(), "host-secrets.json")
	s := importing{file: FileSecrets{Path: path}, legacy: fakeLegacy{err: errors.New("no answer"), reads: &reads}}
	if _, _, err := s.Get(AccountAPIKey); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("Get error = %v; want the import's failure", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed import left the file: %v", err)
	}
}

func TestOpenSecretsFileStoreSkipsTheKeychain(t *testing.T) {
	t.Setenv("BOTTOWN_SECRET_STORE", "file")
	if _, ok := OpenSecrets("x", t.TempDir()).(FileSecrets); !ok {
		t.Error("BOTTOWN_SECRET_STORE=file did not give the plain file store")
	}
}
