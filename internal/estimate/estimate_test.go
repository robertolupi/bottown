package estimate

import (
	"strings"
	"testing"
)

func valid() Input {
	return Input{Size: "m", DecidingFactor: "one new operation", Confidence: "Medium", Scope: "the issue as filed",
		Boundaries: []string{"CLI and server", " "}, Behaviours: []string{"sized", "already sized"}}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Input)
		want string // a substring of the error; "" for none
	}{
		{"size", func(*Input) {}, ""},
		{"label spelling", func(in *Input) { in.Size = "size/XL" }, ""},
		{"unknown size", func(in *Input) { in.Size = "XXL" }, "size must be"},
		{"no deciding factor", func(in *Input) { in.DecidingFactor = " " }, "deciding_factor"},
		{"no scope", func(in *Input) { in.Scope = "" }, "scope"},
		{"no confidence", func(in *Input) { in.Confidence = "" }, "confidence"},
		{"bad confidence", func(in *Input) { in.Confidence = "sure" }, "confidence"},
		{"fence in text", func(in *Input) { in.Behaviours = []string{"```bottown-estimate"} }, "code fences"},
		{"clarification", func(in *Input) {
			*in = Input{Size: "Needs-Clarification", Questions: []string{"what is done?"}}
		}, ""},
		{"clarification without questions", func(in *Input) {
			*in = Input{Size: NeedsClarification, Questions: []string{" "}}
		}, "questions"},
	} {
		in := valid()
		tc.edit(&in)
		in.Normalize()
		err := in.Validate()
		if tc.want == "" && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		} else if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	in := valid()
	in.Normalize()
	if in.Size != M || in.Confidence != Medium || len(in.Boundaries) != 1 {
		t.Errorf("normalized = %+v", in)
	}
	if in.Label() != "size/M" {
		t.Errorf("label = %q", in.Label())
	}
	c := Input{Size: "NEEDS-CLARIFICATION"}
	c.Normalize()
	if c.Size != NeedsClarification || c.Label() != ClarificationLabel {
		t.Errorf("clarification = %q, label %q", c.Size, c.Label())
	}
}

func TestCommentRoundTrip(t *testing.T) {
	in := valid()
	in.Normalize()
	rec := Record{Version: Version, Guide: "3f9a0c1b2d4e", Run: "r-1", Actor: "claude", Family: "anthropic",
		Role: "implement", Size: in.Size, Applied: true, Confidence: in.Confidence}
	body := Comment(in, rec, "rlupi")
	for _, want := range []string{"**Estimate: size/M**, medium confidence (claude, anthropic)", "**Deciding factor:** one new operation",
		"**Boundaries:**\n\n- CLI and server", "```bottown-estimate\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Not applied") || strings.Contains(body, "@rlupi") {
		t.Errorf("applied size comment:\n%s", body)
	}
	got, ok := Parse(body)
	if !ok || got != rec {
		t.Errorf("Parse = %+v, %v; want %+v", got, ok, rec)
	}
	if f := DecidingFactor(body); f != in.DecidingFactor {
		t.Errorf("DecidingFactor = %q, want %q", f, in.DecidingFactor)
	}

	rec.Applied, rec.Existing = false, "size/L"
	body = Comment(in, rec, "rlupi")
	if !strings.Contains(body, "_Not applied: the issue is already size/L._") {
		t.Errorf("second opinion:\n%s", body)
	}
	if got, ok := Parse(body); !ok || got != rec {
		t.Errorf("Parse = %+v, %v; want %+v", got, ok, rec)
	}
}

func TestCommentClarification(t *testing.T) {
	in := Input{Size: NeedsClarification, Questions: []string{"What is done?", "Which repo?"}}
	rec := Record{Version: Version, Actor: "claude", Role: "implement", Size: in.Size, Applied: true}
	body := Comment(in, rec, "rlupi")
	if !strings.Contains(body, "@rlupi, this issue cannot be sized as written:\n\n- What is done?\n- Which repo?\n") {
		t.Errorf("clarification:\n%s", body)
	}
	if strings.Contains(body, "Open questions") {
		t.Errorf("questions listed twice:\n%s", body)
	}
	if f := DecidingFactor(body); f != "" {
		t.Errorf("DecidingFactor = %q, want none", f)
	}
	if body := Comment(in, rec, ""); strings.Contains(body, "@") {
		t.Errorf("owner's own estimate mentions someone:\n%s", body)
	}
}

func TestParse(t *testing.T) {
	block := func(s string) string { return "```bottown-estimate\n" + s + "\n```\n" }
	for _, tc := range []struct {
		name, body string
		ok         bool
		size       string
	}{
		{"none", "just a comment", false, ""},
		{"other fence", "```toml\nversion = 1\nsize = \"S\"\n```", false, ""},
		{"unclosed", "```bottown-estimate\nversion = 1\nsize = \"S\"\n", false, ""},
		{"unknown version", block("version = 2\nsize = \"S\""), false, ""},
		{"not toml", block("size: S"), false, ""},
		{"no size", block("version = 1"), false, ""},
		{"last wins", block("version = 1\nsize = \"S\"") + "text\n" + block("version = 1\nsize = \"L\""), true, "L"},
		{"crlf", strings.ReplaceAll(block("version = 1\nsize = \"M\""), "\n", "\r\n"), true, "M"},
	} {
		rec, ok := Parse(tc.body)
		if ok != tc.ok || rec.Size != tc.size {
			t.Errorf("%s: Parse = %+v, %v; want size %q, %v", tc.name, rec, ok, tc.size, tc.ok)
		}
	}
}

func TestSizeLabel(t *testing.T) {
	if got := SizeLabel([]string{"mode/ask", "size/L"}); got != "size/L" {
		t.Errorf("SizeLabel = %q", got)
	}
	if got := SizeLabel([]string{"needs/clarification"}); got != "" {
		t.Errorf("SizeLabel = %q", got)
	}
}
