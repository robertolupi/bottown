package hostctl

import (
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
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Get took %s; the timeout is 200ms", d)
	}
	for _, want := range []string{"bottown-test/api-key", "no answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestKeychainResults(t *testing.T) {
	found := Keychain{Service: "s", Bin: fakeSecurity(t, `echo "secret"`)}
	if v, ok, err := found.Get("a"); err != nil || !ok || v != "secret" {
		t.Errorf("Get = %q, %v, %v; want secret, true, nil", v, ok, err)
	}
	missing := Keychain{Service: "s", Bin: fakeSecurity(t, "exit 44")}
	if _, ok, err := missing.Get("a"); err != nil || ok {
		t.Errorf("Get of a missing item = %v, %v; want false, nil", ok, err)
	}
	if err := missing.Delete("a"); err != nil {
		t.Errorf("Delete of a missing item: %v", err)
	}
	failing := Keychain{Service: "s", Bin: fakeSecurity(t, "echo denied >&2; exit 51")}
	err := failing.Set("a", "hunter2")
	if err == nil || !strings.Contains(err.Error(), "s/a") || !strings.Contains(err.Error(), "denied") {
		t.Errorf("Set error = %v; want the item and security's message", err)
	}
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Set error %q leaks the value", err)
	}
}
