package milestone

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// md writes plan files readably: ”' stands for ```.
func md(s string) []byte { return []byte(strings.ReplaceAll(s, "'''", "```")) }

const validPlan = `+++
title = "Milestones: plan, check, apply and run"
kickoff = 74
design = "docs/design/milestones.md"
+++

Overview prose
for the epic.

'''sh
## not a section: inside a fence
'''

## Plan format and offline check

'''issue
key = "plan-format"
depends = []
'''

Implement the parser.

### Acceptance criteria

~~~~markdown
## Not a heading either
'''issue
key = "not-a-block"
'''
~~~~

- Rejects cycles.


## Plan run kind

'''issue
key = "plan-run"
depends = ["plan-format", "#64"]
'''
Add policy.Plan.

## Apply ##

'''issue
key = "apply"
depends = ["plan-run", "plan-format"]
'''

Apply the plan.
`

func TestCheckValid(t *testing.T) {
	p, err := Check("docs/milestones/milestones.md", md(validPlan))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if p.Slug != "milestones" || p.Title != "Milestones: plan, check, apply and run" || p.Kickoff != 74 ||
		p.Design != "docs/design/milestones.md" || p.DesignLine != 4 {
		t.Errorf("front matter: %+v", p)
	}
	if want := "Overview prose\nfor the epic.\n\n```sh\n## not a section: inside a fence\n```"; p.Overview != want {
		t.Errorf("overview = %q, want %q", p.Overview, want)
	}
	var keys, titles []string
	for _, is := range p.Issues {
		keys, titles = append(keys, is.Key), append(titles, is.Title)
	}
	if !slices.Equal(keys, []string{"plan-format", "plan-run", "apply"}) {
		t.Errorf("keys = %q", keys)
	}
	if !slices.Equal(titles, []string{"Plan format and offline check", "Plan run kind", "Apply"}) {
		t.Errorf("titles = %q", titles)
	}
	first := p.Issues[0]
	if first.Line != 14 || first.Depends == nil || len(first.Depends) != 0 {
		t.Errorf("first issue: %+v", first)
	}
	wantBody := "Implement the parser.\n\n### Acceptance criteria\n\n~~~~markdown\n## Not a heading either\n```issue\nkey = \"not-a-block\"\n```\n~~~~\n\n- Rejects cycles."
	if first.Body != wantBody {
		t.Errorf("body = %q, want %q", first.Body, wantBody)
	}
	if got := p.Issues[1]; !slices.Equal(got.Depends, []string{"plan-format", "#64"}) || got.Body != "Add policy.Plan." {
		t.Errorf("second issue: %+v", got)
	}
}

func TestCheckCRLFAndNoDesign(t *testing.T) {
	src := "+++\r\ntitle = \"T\"\r\nkickoff = 1\r\n+++\r\n## A\r\n```issue\r\nkey = \"a\"\r\n```\r\nBody.\r\n"
	p, err := Check("a.md", []byte(src))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if p.Design != "" || p.Overview != "" || p.Issues[0].Body != "Body." {
		t.Errorf("plan: %+v", p)
	}
}

// head is a valid front matter for the rejection cases.
const head = "+++\ntitle = \"T\"\nkickoff = 7\n+++\n"

func TestCheckRejects(t *testing.T) {
	for _, tc := range []struct {
		name, file, src string
		want            []string // substrings of the diagnostics
	}{
		{"bad slug", "docs/milestones/Bad_Slug.md", head + "## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{`plan slug "Bad_Slug"`}},
		{"no front matter", "", "## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"p.md:1: the file must start with +++"}},
		{"front matter not on line 1", "", "\n" + head + "## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"p.md:1: the file must start with +++"}},
		{"unclosed front matter", "", "+++\ntitle = \"T\"\nkickoff = 7\n## A\n",
			[]string{"never closed with +++"}},
		{"front matter syntax", "", "+++\ntitle = \"T\"\nkickoff = \n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"p.md:3: toml:"}},
		{"front matter unknown field", "", "+++\ntitle = \"T\"\nkickoff = 7\nmilestone = 3\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{`p.md:4: unknown field "milestone"`}},
		{"missing title and kickoff", "", "+++\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"title is required", "kickoff is required"}},
		{"negative kickoff", "", "+++\ntitle = \"T\"\nkickoff = -3\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"kickoff is required and must be a positive"}},
		{"string kickoff", "", "+++\ntitle = \"T\"\nkickoff = \"7\"\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"p.md:3: toml: cannot decode TOML string"}},
		{"design escapes", "", "+++\ntitle = \"T\"\nkickoff = 7\ndesign = \"../x.md\"\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{`p.md:4: front matter: design "../x.md" must not contain ..`}},
		{"design absolute", "", "+++\ntitle = \"T\"\nkickoff = 7\ndesign = \"/x.md\"\n+++\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{`design "/x.md" must be relative`}},
		{"level-1 heading", "", head + "# Top\n## A\n'''issue\nkey = \"a\"\n'''\nBody.\n# Inside\n",
			[]string{`p.md:5: level-1 heading "Top"`, `p.md:11: level-1 heading "Inside"`}},
		{"no sections", "", head + "Just prose.\n'''\n## fenced\n'''\n",
			[]string{"no ## sections"}},
		{"empty heading", "", head + "##\n'''issue\nkey = \"a\"\n'''\nBody.\n",
			[]string{"p.md:5: a ## heading needs a title"}},
		{"missing issue block", "", head + "## A\nBody first.\n'''issue\nkey = \"a\"\n'''\n",
			[]string{`p.md:5: "## A": the section must start with an` + " ```issue"}},
		{"other fence first", "", head + "## A\n'''toml\nkey = \"a\"\n'''\nBody.\n",
			[]string{`"## A": the section must start with`}},
		{"second issue block", "", head + "## A\n'''issue\nkey = \"a\"\n'''\nBody.\n'''issue\nkey = \"b\"\n'''\n",
			[]string{`p.md:10: "## A": a second issue block`}},
		{"unclosed issue block", "", head + "## A\n'''issue\nkey = \"a\"\n",
			[]string{"p.md:6: \"## A\": fenced code block is never closed"}},
		{"unclosed fence in body", "", head + "## A\n'''issue\nkey = \"a\"\n'''\nBody.\n~~~\n## B\n",
			[]string{"p.md:10: \"## A\": fenced code block is never closed"}},
		{"issue unknown field", "", head + "## A\n'''issue\nkey = \"a\"\ntitle = \"x\"\n'''\nBody.\n",
			[]string{`p.md:8: "## A": unknown field "title"`}},
		{"issue syntax", "", head + "## A\n'''issue\nkey = a\n'''\nBody.\n",
			[]string{`p.md:7: "## A": toml:`}},
		{"missing key", "", head + "## A\n'''issue\ndepends = []\n'''\nBody.\n",
			[]string{`p.md:6: "## A": issue block: key is required`}},
		{"empty body", "", head + "## A\n'''issue\nkey = \"a\"\n'''\n\n  \n## B\n'''issue\nkey = \"b\"\n'''\nB.\n",
			[]string{"p.md:5: a: the issue body is empty"}},
		{"invalid key", "", head + "## A\n'''issue\nkey = \"Not_Kebab\"\n'''\nBody.\n",
			[]string{`Not_Kebab: key "Not_Kebab" must match`}},
		{"duplicate key", "", head + "## A\n'''issue\nkey = \"a\"\n'''\nA.\n## B\n'''issue\nkey = \"a\"\n'''\nB.\n",
			[]string{"p.md:10: a: duplicate key: also the key of the section on line 5"}},
		{"unknown dependency", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"b\"]\n'''\nA.\n",
			[]string{`p.md:5: a: dependency "b" is not a key in this plan`}},
		{"kickoff dependency", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"#7\"]\n'''\nA.\n",
			[]string{`a: dependency "#7" is the kickoff issue`}},
		{"bad issue references", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"#0\", \"#x\", \"#\", \"#-1\", \"#012\"]\n'''\nA.\n",
			[]string{`"#0": an issue reference`, `"#x": an issue reference`, `"#": an issue reference`, `"#-1": an issue reference`, `"#012": an issue reference`}},
		{"self dependency", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"a\"]\n'''\nA.\n",
			[]string{"a: depends on itself"}},
		{"duplicate dependency", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"#3\", \"b\", \"#3\"]\n'''\nA.\n## B\n'''issue\nkey = \"b\"\n'''\nB.\n",
			[]string{`a: duplicate dependency "#3"`}},
		{"two-node cycle", "", head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"b\"]\n'''\nA.\n## B\n'''issue\nkey = \"b\"\ndepends = [\"a\"]\n'''\nB.\n",
			[]string{"p.md:5: a: dependency cycle: a -> b -> a"}},
		{"multi-node cycle", "", head +
			"## Root\n'''issue\nkey = \"root\"\ndepends = [\"a\"]\n'''\nR.\n" +
			"## A\n'''issue\nkey = \"a\"\ndepends = [\"b\", \"#3\"]\n'''\nA.\n" +
			"## B\n'''issue\nkey = \"b\"\ndepends = [\"c\"]\n'''\nB.\n" +
			"## C\n'''issue\nkey = \"c\"\ndepends = [\"a\"]\n'''\nC.\n",
			[]string{"a: dependency cycle: a -> b -> c -> a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = "p.md"
			}
			_, err := Check(file, md(tc.src))
			var ds Diagnostics
			if !errors.As(err, &ds) || len(ds) == 0 {
				t.Fatalf("err = %v, want Diagnostics", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("diagnostics:\n%v\nwant one containing %q", err, w)
				}
			}
		})
	}
}

func TestCheckCycleReportedOnce(t *testing.T) {
	src := head + "## A\n'''issue\nkey = \"a\"\ndepends = [\"b\"]\n'''\nA.\n## B\n'''issue\nkey = \"b\"\ndepends = [\"a\"]\n'''\nB.\n"
	_, err := Check("p.md", md(src))
	if n := strings.Count(err.Error(), "dependency cycle"); n != 1 {
		t.Errorf("%d cycle diagnostics, want 1:\n%v", n, err)
	}
}

func TestSlugOf(t *testing.T) {
	for in, want := range map[string]string{
		"docs/milestones/web-ui.md": "web-ui",
		"web-ui.md":                 "web-ui",
		"./x/y.md":                  "y",
	} {
		if got := SlugOf(in); got != want {
			t.Errorf("SlugOf(%q) = %q, want %q", in, got, want)
		}
	}
}
