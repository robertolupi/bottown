package runs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// The sizing guide (docs/reference/sizing.md): how an agent sizes an issue,
// rendered from prompts/sizing.md into the prompts of runs that size issues
// (plan, implement and estimate runs). Estimates record the version of the guide they
// followed, so estimates made by different texts can be told apart.

// SizingGuideVersion is the first 12 hex digits of the SHA-256 of
// prompts/sizing.md as built into the binary: it changes whenever the
// guide's text does, with nothing to bump by hand.
var SizingGuideVersion = sizingVersion()

func sizingVersion() string {
	b, err := promptFS.ReadFile("prompts/sizing.md")
	if err != nil {
		panic(err) // embedded; cannot be missing
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// SizingGuide renders the sizing guide on its own.
func SizingGuide() (string, error) {
	var b bytes.Buffer
	if err := prompts.ExecuteTemplate(&b, "sizing", nil); err != nil {
		return "", err
	}
	return b.String(), nil
}
