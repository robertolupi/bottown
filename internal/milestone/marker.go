package milestone

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// A Marker is the provenance line `apply` writes at the bottom of each issue
// it files (docs/design/milestones.md §2.6):
//
//	<!-- bottown:milestone v=1 plan="<slug>" role="epic" sha="<hex16>" -->
//	<!-- bottown:milestone v=1 plan="<slug>" key="<key>" sha="<hex16>" -->
//
// Readers accept a marker only on an issue the owner filed: see Trusted.
type Marker struct {
	Plan string
	Epic bool   // role="epic"
	Key  string // a child issue's key; empty on the epic
	SHA  string // Hash of the title and body apply last wrote
}

func (m Marker) String() string {
	role := fmt.Sprintf("key=%q", m.Key)
	if m.Epic {
		role = `role="epic"`
	}
	return fmt.Sprintf("<!-- bottown:milestone v=1 plan=%q %s sha=%q -->", m.Plan, role, m.SHA)
}

var markerRE = regexp.MustCompile(`^<!-- bottown:milestone v=1 plan="([a-z0-9]+(?:-[a-z0-9]+)*)" (?:role="epic"|key="([a-z0-9]+(?:-[a-z0-9]+)*)") sha="([0-9a-f]{16})" -->$`)

// ParseMarker finds the marker on the last line of an issue body. It returns
// the marker and the body without it, trimmed as Hash expects; ok is false if
// the body ends in no valid marker.
func ParseMarker(body string) (m Marker, content string, ok bool) {
	body = normalize(body)
	i := strings.LastIndexByte(body, '\n')
	last := strings.TrimSpace(body[i+1:])
	sub := markerRE.FindStringSubmatch(last)
	if sub == nil {
		return Marker{}, body, false
	}
	m = Marker{Plan: sub[1], Key: sub[2], SHA: sub[3], Epic: sub[2] == ""}
	if i < 0 {
		return m, "", true
	}
	return m, normalize(body[:i]), true
}

// Trusted reports whether a marker on an issue filed by poster counts: only
// the owner's issues carry trusted markers, because actors can file issues
// but not edit them.
func Trusted(poster, owner string) bool { return owner != "" && poster == owner }

// Hash is a marker's sha: the first 16 hex characters of the SHA-256 of the
// title and the body without the marker.
func Hash(title, content string) string {
	sum := sha256.Sum256([]byte(title + "\n\n" + normalize(content)))
	return hex.EncodeToString(sum[:])[:16]
}

// withMarker is a managed issue body: its content, then its marker.
func withMarker(content string, m Marker) string {
	content = normalize(content)
	if content == "" {
		return m.String()
	}
	return content + "\n\n" + m.String()
}

// normalize is the canonical form of an issue body for comparison: LF line
// endings and no trailing white space.
func normalize(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n")
}
