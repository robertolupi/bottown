package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/server"
)

func testInstance() config.Instance {
	return config.Instance{
		Name:        "prod",
		ForgejoPort: 3030,
		ServerPort:  3031,
		Image:       "bottown:96032e2a5e7c",
	}
}

func TestPrintStatusNonInteractive(t *testing.T) {
	inst := testInstance()
	h := &server.Health{
		Forgejo:        "ready",
		Reconciled:     true,
		Version:        "96032e2a5e7c",
		ForgejoVersion: "15.0.11+gitea-1.22.0",
	}

	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	printStatus(&buf, r, inst, "git config bottown.instance", "running", h, nil)

	got := buf.String()
	want := strings.Join([]string{
		"instance  prod (from git config bottown.instance)",
		"container bottown-prod (running)",
		"image     bottown:96032e2a5e7c",
		"forgejo   http://127.0.0.1:3030",
		"bottown   http://127.0.0.1:3031",
		"health    forgejo=ready reconciled=true version=96032e2a5e7c forgejo_version=15.0.11+gitea-1.22.0",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("plain output mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-interactive output must not contain ANSI escape codes: %q", got)
	}
}

func TestPrintStatusInteractive(t *testing.T) {
	inst := testInstance()
	h := &server.Health{
		Forgejo:        "ready",
		Reconciled:     true,
		Version:        "96032e2a5e7c",
		ForgejoVersion: "15.0.11+gitea-1.22.0",
		Error:          "something went wrong",
	}

	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	r.SetColorProfile(termenv.TrueColor)
	printStatus(&buf, r, inst, "git config bottown.instance", "running", h, nil)

	got := buf.String()

	// Verify the bold tags wrap the first words
	expectedBoldWords := []string{"instance", "container", "image", "forgejo", "bottown", "health", "error"}
	for _, word := range expectedBoldWords {
		boldWord := "\x1b[1m" + word + "\x1b[0m"
		if !strings.Contains(got, boldWord) {
			t.Errorf("interactive output should contain bold word %q:\n%s", boldWord, got)
		}
	}

	// Stripping ANSI should produce exact clean alignment
	stripped := stripANSI(got)
	wantStripped := strings.Join([]string{
		"instance  prod (from git config bottown.instance)",
		"container bottown-prod (running)",
		"image     bottown:96032e2a5e7c",
		"forgejo   http://127.0.0.1:3030",
		"bottown   http://127.0.0.1:3031",
		"health    forgejo=ready reconciled=true version=96032e2a5e7c forgejo_version=15.0.11+gitea-1.22.0",
		"error     something went wrong",
		"",
	}, "\n")

	if stripped != wantStripped {
		t.Fatalf("stripped interactive output mismatch:\ngot:\n%s\nwant:\n%s", stripped, wantStripped)
	}
}

func TestPrintStatusUnreachable(t *testing.T) {
	inst := testInstance()
	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	r.SetColorProfile(termenv.TrueColor)
	printStatus(&buf, r, inst, "default", "running", nil, errors.New("connection refused"))

	got := buf.String()
	boldHealth := "\x1b[1mhealth\x1b[0m"
	if !strings.Contains(got, boldHealth) {
		t.Errorf("interactive unreachable output should contain bold %q", boldHealth)
	}
	if !strings.Contains(got, "unreachable: connection refused") {
		t.Errorf("expected unreachable message in output:\n%s", got)
	}
}

func TestPrintStatusStopped(t *testing.T) {
	inst := testInstance()
	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	printStatus(&buf, r, inst, "default", "stopped", nil, nil)

	got := buf.String()
	if strings.Contains(got, "health") {
		t.Errorf("stopped container should not output health:\n%s", got)
	}
}
