// Package agents parses a milestone's agent pool and pairs implementers with
// verifiers from different model families. It has no forge or run-store access.
package agents

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

// Entry is one actor in a pool. Limit is the most live runs the entry may
// have; zero means unlimited. Model is the optional harness model pin.
type Entry struct {
	Actor  string
	Model  string
	Family string
	Limit  int
}

// String returns the ACTOR[:MODEL] form used by flow.Options.
func (e Entry) String() string {
	if e.Model != "" {
		return e.Actor + ":" + e.Model
	}
	return e.Actor
}

// Pair assigns an implementer and a verifier.
type Pair struct {
	Implement Entry
	Verify    Entry
}

// Pool is an ordered set of entries. Its pairing cursor and removals last for
// the invocation that owns it.
type Pool struct {
	entries []Entry
	removed map[string]bool
	next    int
}

// Parse parses comma-separated actor[:model]=limit entries. Every actor must
// occur in actors at most once in the spec, and every represented family must
// have a potential verifier from another family.
func Parse(spec string, actors []config.Actor) (*Pool, error) {
	if spec == "" {
		return nil, fmt.Errorf("agents spec is empty")
	}
	known := make(map[string]string, len(actors))
	for _, actor := range actors {
		known[actor.Name] = actor.Family
	}
	p := &Pool{removed: map[string]bool{}}
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		entry, err := parseEntry(raw, known)
		if err != nil {
			return nil, err
		}
		if seen[entry.Actor] {
			return nil, fmt.Errorf("agents entry %q: actor %q appears more than once", raw, entry.Actor)
		}
		seen[entry.Actor] = true
		p.entries = append(p.entries, entry)
	}
	for _, entry := range p.entries {
		if !hasOtherFamily(p.entries, entry.Family) {
			return nil, fmt.Errorf("agents pool: family %q cannot be verified by another family", entry.Family)
		}
	}
	return p, nil
}

func parseEntry(raw string, known map[string]string) (Entry, error) {
	choice, count, ok := strings.Cut(raw, "=")
	if !ok || choice == "" || count == "" {
		return Entry{}, fmt.Errorf("agents entry %q: want actor[:model]=N", raw)
	}
	actor, model, err := runs.SplitActorSpec(choice)
	if err != nil {
		return Entry{}, fmt.Errorf("agents entry %q: %w", raw, err)
	}
	family, ok := known[actor]
	if !ok {
		return Entry{}, fmt.Errorf("agents entry %q: unknown actor %q", raw, actor)
	}
	limit := 0
	if count != "*" {
		limit, err = strconv.Atoi(count)
		if err != nil || limit <= 0 {
			return Entry{}, fmt.Errorf("agents entry %q: limit must be a positive integer or *", raw)
		}
	}
	return Entry{Actor: actor, Model: model, Family: family, Limit: limit}, nil
}

func hasOtherFamily(entries []Entry, family string) bool {
	for _, entry := range entries {
		if entry.Family != family {
			return true
		}
	}
	return false
}

// Entries returns the pool in spec order, including removed entries.
func (p *Pool) Entries() []Entry {
	return append([]Entry(nil), p.entries...)
}

// Remove excludes actor's entry from subsequent pairing. It reports whether
// the entry was present and not already removed.
func (p *Pool) Remove(actor string) bool {
	for _, entry := range p.entries {
		if entry.Actor == actor {
			already := p.removed[actor]
			p.removed[actor] = true
			return !already
		}
	}
	return false
}

// Next returns the next pair with spare capacity, or false when none exists.
// live holds each entry's live run count by actor name. Implementers advance
// round-robin through spec order; verifiers start at the following entry.
func (p *Pool) Next(live map[string]int) (Pair, bool) {
	for offset := range len(p.entries) {
		implement := (p.next + offset) % len(p.entries)
		if !p.available(implement, live) {
			continue
		}
		for verifyOffset := 1; verifyOffset < len(p.entries); verifyOffset++ {
			verify := (implement + verifyOffset) % len(p.entries)
			if p.entries[verify].Family == p.entries[implement].Family || !p.available(verify, live) {
				continue
			}
			p.next = (implement + 1) % len(p.entries)
			return Pair{Implement: p.entries[implement], Verify: p.entries[verify]}, true
		}
	}
	return Pair{}, false
}

func (p *Pool) available(index int, live map[string]int) bool {
	entry := p.entries[index]
	return !p.removed[entry.Actor] && (entry.Limit == 0 || live[entry.Actor] < entry.Limit)
}
