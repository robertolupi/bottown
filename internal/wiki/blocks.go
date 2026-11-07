// Package wiki is the pure part of bottown's wiki store: generated blocks,
// links between pages, slug resolution, and the generator that rewrites
// every generated block from the wiki's own content. It reads no clock, no
// git and no forge: the server (internal/server/wiki.go) feeds it a tree
// and commits what it returns.
//
// A generated block is the text between two markers:
//
//	<!-- bottown:backlinks:generated -->
//	…
//	<!-- /bottown:backlinks:generated -->
//
// Blocks do not nest, and their names come from a fixed registry
// (Generators). Whatever a writer puts inside a block is replaced by the
// generator's output, so agents never maintain blocks.
package wiki

import (
	"fmt"
	"regexp"
	"strings"
)

// markerRE matches an opening or closing block marker.
var markerRE = regexp.MustCompile(`<!-- (/?)bottown:([a-z0-9-]+):generated -->`)

// Segment is a run of a page: plain text, or a generated block.
type Segment struct {
	Text  string // the text; for a block, its body between the markers
	Block string // the block's name; empty for plain text
}

// Open and Close are a block's markers.
func Open(name string) string  { return "<!-- bottown:" + name + ":generated -->" }
func Close(name string) string { return "<!-- /bottown:" + name + ":generated -->" }

// Parse splits a page into text and generated blocks. A page whose markers
// do not pair up (a close without an open, an open inside a block, a close
// for another block, an open never closed) is mangled: Parse returns an
// error saying where, and the caller leaves the page alone.
func Parse(content string) ([]Segment, error) {
	var segs []Segment
	pos := 0
	open := "" // name of the block being read
	bodyStart := 0
	for _, m := range markerRE.FindAllStringSubmatchIndex(content, -1) {
		closing := content[m[2]:m[3]] == "/"
		name := content[m[4]:m[5]]
		line := 1 + strings.Count(content[:m[0]], "\n")
		switch {
		case !closing && open != "":
			return nil, fmt.Errorf("line %d: block %q opens inside block %q (blocks do not nest)", line, name, open)
		case !closing:
			if m[0] > pos {
				segs = append(segs, Segment{Text: content[pos:m[0]]})
			}
			open, bodyStart = name, m[1]
		case open == "":
			return nil, fmt.Errorf("line %d: block %q closes but was never opened", line, name)
		case name != open:
			return nil, fmt.Errorf("line %d: block %q closes inside block %q", line, name, open)
		default:
			segs = append(segs, Segment{Block: open, Text: content[bodyStart:m[0]]})
			open, pos = "", m[1]
		}
	}
	if open != "" {
		return nil, fmt.Errorf("block %q is never closed", open)
	}
	if pos < len(content) {
		segs = append(segs, Segment{Text: content[pos:]})
	}
	return segs, nil
}

// Join is the page Parse split; a block's body goes between its markers.
func Join(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Block == "" {
			b.WriteString(s.Text)
			continue
		}
		b.WriteString(Open(s.Block))
		b.WriteString(s.Text)
		b.WriteString(Close(s.Block))
	}
	return b.String()
}

// Text is a page without its generated blocks: what its writers wrote. Links
// are read from it, so a block's output never feeds a block.
func Text(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Block == "" {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// Normalize empties every generated block in content, keeping the markers
// where they are, so a write can send blocks in any state (stale, empty,
// rewritten by mistake) and the generator fills them. It refuses mangled
// markers and block names outside the registry: a writer sending those
// made a mistake worth hearing about.
func Normalize(content string) (string, error) {
	segs, err := Parse(content)
	if err != nil {
		return "", err
	}
	for i, s := range segs {
		if s.Block == "" {
			continue
		}
		if Lookup(s.Block) == nil {
			return "", fmt.Errorf("unknown generated block %q (known: %s)", s.Block, strings.Join(Names(), ", "))
		}
		segs[i].Text = ""
	}
	return Join(segs), nil
}
