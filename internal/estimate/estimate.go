// Package estimate is an issue's size estimate as bottown records it: what
// an agent submits with `bottown issue size` (Input), and the comment the
// server posts for it, whose fenced bottown-estimate block (Record) later
// tools parse (docs/reference/sizing.md).
package estimate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// The outcomes of an estimate: a size, or that the issue cannot be sized
// as written.
const (
	S                  = "S"
	M                  = "M"
	L                  = "L"
	XL                 = "XL"
	NeedsClarification = "needs-clarification"
)

// Confidences.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// ClarificationLabel marks an issue an estimate found unclear; the owner
// removes it once the issue is clarified.
const ClarificationLabel = "needs/clarification"

// SizeLabelPrefix starts every size label (size/S … size/XL).
const SizeLabelPrefix = "size/"

// Version is the record format's version.
const Version = 1

// Fence is the info string of the record's fenced block.
const Fence = "bottown-estimate"

// Input is an estimate as an agent submits it: the fields the sizing guide
// asks for (internal/runs/prompts/sizing.md).
type Input struct {
	Size           string   `json:"size" toml:"size" jsonschema:"S, M, L, XL or needs-clarification"`
	DecidingFactor string   `json:"deciding_factor,omitempty" toml:"deciding_factor" jsonschema:"the factor that decides the size, in one sentence"`
	Boundaries     []string `json:"boundaries,omitempty" toml:"boundaries" jsonschema:"the contracts the change crosses or adds"`
	Behaviours     []string `json:"behaviours,omitempty" toml:"behaviours" jsonschema:"the cases the change must decide"`
	Novelty        []string `json:"novelty,omitempty" toml:"novelty" jsonschema:"the existing pattern the change follows, or that it follows none"`
	Questions      []string `json:"questions,omitempty" toml:"questions" jsonschema:"open questions; for needs-clarification, the questions for the owner"`
	Confidence     string   `json:"confidence,omitempty" toml:"confidence" jsonschema:"high, medium or low"`
	Scope          string   `json:"scope,omitempty" toml:"scope" jsonschema:"the scope sized, with any assumptions"`
}

// Normalize trims the input's text, and spells its size and confidence the
// canonical way (size in upper case, confidence in lower).
func (in *Input) Normalize() {
	in.Size = strings.TrimSpace(in.Size)
	if !strings.EqualFold(in.Size, NeedsClarification) {
		in.Size = strings.ToUpper(strings.TrimPrefix(strings.ToLower(in.Size), SizeLabelPrefix))
	} else {
		in.Size = NeedsClarification
	}
	in.Confidence = strings.ToLower(strings.TrimSpace(in.Confidence))
	in.DecidingFactor = strings.TrimSpace(in.DecidingFactor)
	in.Scope = strings.TrimSpace(in.Scope)
	for _, l := range []*[]string{&in.Boundaries, &in.Behaviours, &in.Novelty, &in.Questions} {
		var kept []string
		for _, s := range *l {
			if s = strings.TrimSpace(s); s != "" {
				kept = append(kept, s)
			}
		}
		*l = kept
	}
}

// Validate checks a normalized input: a size needs its deciding factor,
// confidence and scope; needs-clarification needs its questions.
func (in Input) Validate() error {
	switch in.Size {
	case S, M, L, XL:
		if in.DecidingFactor == "" {
			return errors.New("deciding_factor is required with a size")
		}
		if in.Scope == "" {
			return errors.New("scope is required with a size")
		}
		switch in.Confidence {
		case High, Medium, Low:
		default:
			return fmt.Errorf("confidence must be high, medium or low, not %q", in.Confidence)
		}
	case NeedsClarification:
		if len(in.Questions) == 0 {
			return errors.New("questions are required with needs-clarification")
		}
		switch in.Confidence {
		case "", High, Medium, Low:
		default:
			return fmt.Errorf("confidence must be high, medium or low, not %q", in.Confidence)
		}
	default:
		return fmt.Errorf("size must be S, M, L, XL or needs-clarification, not %q", in.Size)
	}
	for _, s := range append([]string{in.DecidingFactor, in.Scope}, slices.Concat(in.Boundaries, in.Behaviours, in.Novelty, in.Questions)...) {
		if strings.Contains(s, "```") || strings.Contains(s, "~~~") {
			return errors.New("estimate text may not contain code fences")
		}
	}
	return nil
}

// Label is the label an estimate applies: the size's, or the
// needs-clarification one.
func (in Input) Label() string {
	if in.Size == NeedsClarification {
		return ClarificationLabel
	}
	return SizeLabelPrefix + in.Size
}

// Record is the machine-readable part of an estimate's comment: who made
// it, in which run, by which guide, and whether it set the label.
type Record struct {
	Version int    `toml:"version"`
	Guide   string `toml:"guide"`
	Run     string `toml:"run,omitempty"`
	Actor   string `toml:"actor"`
	Family  string `toml:"family,omitempty"`
	Role    string `toml:"role"`
	SHA     string `toml:"sha,omitempty"`
	Size    string `toml:"size"`
	// Applied says the estimate set the issue's label; false when the issue
	// was already sized, and the estimate is a second opinion.
	Applied    bool   `toml:"applied"`
	Confidence string `toml:"confidence,omitempty"`
	// Existing is the size label the issue already had, when not Applied.
	Existing string `toml:"existing,omitempty"`
}

// Comment renders an estimate's comment: a summary for the owner, then the
// record's fenced block. owner is the login the questions are addressed to.
func Comment(in Input, rec Record, owner string) string {
	var b strings.Builder
	by := rec.Actor
	if rec.Family != "" {
		by += ", " + rec.Family
	}
	if in.Size == NeedsClarification {
		fmt.Fprintf(&b, "**Estimate: needs clarification** (%s)\n\n", by)
		if owner != "" {
			fmt.Fprintf(&b, "@%s, this issue cannot be sized as written:\n\n", owner)
		} else {
			b.WriteString("This issue cannot be sized as written:\n\n")
		}
		list(&b, in.Questions)
	} else {
		fmt.Fprintf(&b, "**Estimate: %s%s**, %s confidence (%s)\n\n", SizeLabelPrefix, in.Size, in.Confidence, by)
		fmt.Fprintf(&b, "**Deciding factor:** %s\n\n", in.DecidingFactor)
	}
	if !rec.Applied {
		fmt.Fprintf(&b, "_Not applied: the issue is already %s._\n\n", rec.Existing)
	}
	if in.Scope != "" {
		fmt.Fprintf(&b, "**Scope:** %s\n\n", in.Scope)
	}
	for _, f := range []struct {
		name  string
		items []string
	}{{"Boundaries", in.Boundaries}, {"Behaviours", in.Behaviours}, {"Novelty", in.Novelty}} {
		if len(f.items) > 0 {
			fmt.Fprintf(&b, "**%s:**\n\n", f.name)
			list(&b, f.items)
		}
	}
	if in.Size != NeedsClarification && len(in.Questions) > 0 {
		b.WriteString("**Open questions:**\n\n")
		list(&b, in.Questions)
	}
	block, err := toml.Marshal(rec)
	if err != nil {
		panic(err) // a struct of strings, ints and bools always marshals
	}
	fmt.Fprintf(&b, "```%s\n%s```\n", Fence, block)
	return b.String()
}

func list(b *strings.Builder, items []string) {
	for _, s := range items {
		fmt.Fprintf(b, "- %s\n", strings.ReplaceAll(s, "\n", " "))
	}
	b.WriteString("\n")
}

// Parse reads the record from an estimate's comment: the last
// bottown-estimate block in body. It reports false when there is none, or
// when the block is not a record of a known version.
func Parse(body string) (Record, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var block string
	found := false
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```"+Fence {
			continue
		}
		var buf strings.Builder
		j := i + 1
		for ; j < len(lines) && strings.TrimSpace(lines[j]) != "```"; j++ {
			buf.WriteString(lines[j])
			buf.WriteByte('\n')
		}
		if j < len(lines) {
			block, found = buf.String(), true
		}
		i = j
	}
	if !found {
		return Record{}, false
	}
	var rec Record
	if err := toml.Unmarshal([]byte(block), &rec); err != nil || rec.Version != Version || rec.Size == "" {
		return Record{}, false
	}
	return rec, true
}

// SizeLabel is the size label among an issue's labels, or "".
func SizeLabel(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, SizeLabelPrefix) {
			return l
		}
	}
	return ""
}
