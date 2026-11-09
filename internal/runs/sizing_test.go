package runs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSizingGuide(t *testing.T) {
	g, err := SizingGuide()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Size the whole issue**", "**Boundaries:**", "**Behaviours to define:**",
		"**Novelty:**", "**Open questions:**",
		"| **S** |", "| **M** |", "| **L** |", "| **XL** |",
		"`needs-clarification`", "**Record:**",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("sizing guide lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "{{") {
		t.Errorf("sizing guide has template syntax left:\n%s", g)
	}
}

// The version is the hash of the template's text, so it follows any edit.
func TestSizingGuideVersion(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(SizingGuideVersion) {
		t.Fatalf("SizingGuideVersion = %q, want 12 hex digits", SizingGuideVersion)
	}
	b, err := os.ReadFile("prompts/sizing.md")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if want := hex.EncodeToString(sum[:])[:12]; SizingGuideVersion != want {
		t.Errorf("SizingGuideVersion = %s, want %s", SizingGuideVersion, want)
	}
}
