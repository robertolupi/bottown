package milestone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeForge is an in-memory repository for Apply.
type fakeForge struct {
	sha        string
	files      map[string]string
	milestones map[int64]*ForgeMilestone
	issues     map[int64]*ForgeIssue
	deps       map[int64][]int64 // issue → blocked by
	next       int64
	writes     []string
	// failCreate fails the CreateIssue call with that 1-based index, if > 0.
	failCreate int
	creates    int
}

const owner = "owner"

func newFake() *fakeForge {
	f := &fakeForge{
		sha:        "1111111111111111111111111111111111111111",
		files:      map[string]string{"docs/d.md": "design\n"},
		milestones: map[int64]*ForgeMilestone{7: {ID: 7, Title: "M1"}},
		issues:     map[int64]*ForgeIssue{},
		deps:       map[int64][]int64{},
		next:       1,
	}
	f.add(&ForgeIssue{Title: "Kickoff", Poster: owner, Milestone: f.milestones[7]}) // #1
	f.add(&ForgeIssue{Title: "External", Poster: owner})                            // #2
	return f
}

func (f *fakeForge) add(is *ForgeIssue) int64 {
	is.Number = f.next
	f.next++
	f.issues[is.Number] = is
	return is.Number
}

func (f *fakeForge) DefaultBranch(context.Context) (string, string, error) {
	return "main", f.sha, nil
}

func (f *fakeForge) LastCommit(_ context.Context, ref, path string) (string, error) {
	if _, ok := f.files[path]; !ok {
		return "", nil
	}
	return "2222222222222222222222222222222222222222", nil
}

func (f *fakeForge) ReadFile(_ context.Context, ref, path string) ([]byte, error) {
	if ref != f.sha {
		return nil, fmt.Errorf("read %s at %s, not the default branch's head", path, ref)
	}
	s, ok := f.files[path]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(s), nil
}

func (f *fakeForge) Issue(_ context.Context, n int64) (*ForgeIssue, error) {
	is, ok := f.issues[n]
	if !ok {
		return nil, ErrNotFound
	}
	c := *is
	return &c, nil
}

func (f *fakeForge) Issues(context.Context) ([]*ForgeIssue, error) {
	var out []*ForgeIssue
	for n := int64(1); n < f.next; n++ {
		if is, ok := f.issues[n]; ok && !is.PR {
			c := *is
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeForge) CreateIssue(_ context.Context, title, body string, ms int64) (int64, error) {
	f.creates++
	if f.creates == f.failCreate {
		return 0, errors.New("injected failure")
	}
	n := f.add(&ForgeIssue{Title: title, Body: body, Poster: owner, Milestone: f.milestones[ms]})
	f.writes = append(f.writes, fmt.Sprintf("create #%d", n))
	return n, nil
}

func (f *fakeForge) EditIssue(_ context.Context, n int64, title, body string) error {
	f.issues[n].Title, f.issues[n].Body = title, body
	f.writes = append(f.writes, fmt.Sprintf("edit #%d", n))
	return nil
}

func (f *fakeForge) BlockedBy(_ context.Context, n int64) ([]int64, error) {
	return slices.Clone(f.deps[n]), nil
}

func (f *fakeForge) AddBlockedBy(_ context.Context, n, dep int64) error {
	if slices.Contains(f.deps[n], dep) {
		return fmt.Errorf("#%d is already blocked by #%d", n, dep)
	}
	f.deps[n] = append(f.deps[n], dep)
	f.writes = append(f.writes, fmt.Sprintf("dep #%d+#%d", n, dep))
	return nil
}

func (f *fakeForge) RemoveBlockedBy(_ context.Context, n, dep int64) error {
	i := slices.Index(f.deps[n], dep)
	if i < 0 {
		return fmt.Errorf("#%d is not blocked by #%d", n, dep)
	}
	f.deps[n] = slices.Delete(f.deps[n], i, i+1)
	f.writes = append(f.writes, fmt.Sprintf("dep #%d-#%d", n, dep))
	return nil
}

// managed maps the plan's markers on the forge: key ("" for the epic) →
// issue numbers.
func (f *fakeForge) managed(slug string) map[string][]int64 {
	out := map[string][]int64{}
	for n := int64(1); n < f.next; n++ {
		is, ok := f.issues[n]
		if !ok || is.Poster != owner {
			continue
		}
		if m, _, ok := ParseMarker(is.Body); ok && m.Plan == slug {
			out[m.Key] = append(out[m.Key], n)
		}
	}
	return out
}

const planFile = "docs/milestones/demo.md"

const plan1 = `+++
title = "Demo epic"
kickoff = 1
design = "docs/d.md"
+++

The overview.

## Alpha

'''issue
key = "a"
'''

Body of a.

## Bravo

'''issue
key = "b"
depends = ["a", "#2"]
'''

Body of b.

## Charlie

'''issue
key = "c"
depends = ["b"]
'''

Body of c.

## Delta

'''issue
key = "d"
depends = ["c"]
'''

Body of d.
`

func apply(t *testing.T, f *fakeForge, mod func(*ApplyOptions)) (*Report, string, error) {
	t.Helper()
	var out strings.Builder
	o := ApplyOptions{Org: "bottown", Repo: "demo", Owner: owner, Slug: "demo", Out: &out}
	if mod != nil {
		mod(&o)
	}
	rep, err := Apply(context.Background(), f, o)
	return rep, out.String(), err
}

func mustApply(t *testing.T, f *fakeForge, mod func(*ApplyOptions)) (*Report, string) {
	t.Helper()
	rep, out, err := apply(t, f, mod)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	return rep, out
}

func withPlan(src string) *fakeForge {
	f := newFake()
	f.files[planFile] = string(md(src))
	return f
}

func TestApplyFirstAndIdempotent(t *testing.T) {
	f := withPlan(plan1)
	rep, out := mustApply(t, f, nil)
	// a #3, b #4, c #5, d #6, epic #7.
	if got := f.managed("demo"); !slices.Equal(got["a"], []int64{3}) || !slices.Equal(got["d"], []int64{6}) || !slices.Equal(got[""], []int64{7}) {
		t.Fatalf("managed issues: %v\n%s", got, out)
	}
	if rep.Epic != 7 || rep.MainSHA != f.sha || rep.Milestone.ID != 7 {
		t.Errorf("report: %+v", rep)
	}
	for n := int64(3); n <= 7; n++ {
		if is := f.issues[n]; is.Milestone == nil || is.Milestone.ID != 7 {
			t.Errorf("#%d not in milestone 7", n)
		}
	}
	want := map[int64][]int64{4: {3, 2}, 5: {4}, 6: {5}, 7: {3, 4, 5, 6}}
	for n, w := range want {
		if !slices.Equal(f.deps[n], w) {
			t.Errorf("#%d blocked by %v, want %v", n, f.deps[n], w)
		}
	}
	if len(f.deps[3]) != 0 {
		t.Errorf("#3 blocked by %v", f.deps[3])
	}

	b := f.issues[4]
	m, content, ok := ParseMarker(b.Body)
	if !ok || m.Key != "b" || m.Epic || m.SHA != Hash(b.Title, content) || b.Title != "Bravo" {
		t.Errorf("b's marker %+v ok=%v on %q", m, ok, b.Body)
	}
	if !strings.HasPrefix(b.Body, "Body of b.\n\n---\n\n_Filed by `bottown milestone apply` from [`docs/milestones/demo.md`](/bottown/demo/src/branch/main/docs/milestones/demo.md) for kickoff #1. Design: [`docs/d.md`](/bottown/demo/src/branch/main/docs/d.md)._\n\n<!-- bottown:milestone v=1 plan=\"demo\" key=\"b\" sha=") {
		t.Errorf("b's body:\n%s", b.Body)
	}
	epic := f.issues[7]
	for _, s := range []string{
		"_Implementation Epic for kickoff #1, filed by `bottown milestone apply` from [`docs/milestones/demo.md`](/bottown/demo/src/commit/2222222222222222222222222222222222222222/docs/milestones/demo.md) at `222222222222`.",
		"\n\nThe overview.\n\n## Implementation issues\n\n" +
			"- [ ] #3 — Alpha (`a`)\n" +
			"- [ ] #4 — Bravo (`b`, depends on #3, #2)\n" +
			"- [ ] #5 — Charlie (`c`, depends on #4)\n" +
			"- [ ] #6 — Delta (`d`, depends on #5)\n\n" +
			`<!-- bottown:milestone v=1 plan="demo" role="epic" sha="`,
	} {
		if !strings.Contains(epic.Body, s) {
			t.Errorf("epic body lacks %q:\n%s", s, epic.Body)
		}
	}
	if epic.Title != "Demo epic" || strings.Contains(epic.Body, "Flagged") {
		t.Errorf("epic: %q\n%s", epic.Title, epic.Body)
	}
	if rep.Writes != len(f.writes) || rep.Writes != 5+4+4 {
		t.Errorf("writes: report %d, forge %v", rep.Writes, f.writes)
	}

	// Again: nothing to do.
	f.writes = nil
	rep, out = mustApply(t, f, nil)
	if len(f.writes) != 0 || len(rep.Changes) != 0 || len(rep.Flags) != 0 || !strings.Contains(out, "no changes") {
		t.Errorf("re-apply wrote %v, changes %v, flags %v\n%s", f.writes, rep.Changes, rep.Flags, out)
	}
}

func TestApplyDryRun(t *testing.T) {
	f := withPlan(plan1)
	rep, out := mustApply(t, f, func(o *ApplyOptions) { o.DryRun = true })
	if len(f.writes) != 0 || f.next != 3 {
		t.Fatalf("dry run wrote %v", f.writes)
	}
	for _, s := range []string{
		"plan docs/milestones/demo.md on main at 111111111111",
		`milestone "M1" (id 7), kickoff #1`,
		`create (a) in milestone "M1": Alpha`,
		`create the Implementation Epic in milestone "M1": Demo epic`,
		"(b): add blocked by (a)",
		"(b): add blocked by #2",
		"the epic: add blocked by (d)",
		"dry run: nothing written",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("dry run output lacks %q:\n%s", s, out)
		}
	}
	if rep.Writes != 0 || len(rep.Changes) != 5+4+4 {
		t.Errorf("report: %+v", rep)
	}

	// A dry run of an applied, changed plan names the issues.
	mustApply(t, f, nil)
	f.writes = nil
	// A new title changes the epic's checklist too.
	f.files[planFile] = strings.Replace(f.files[planFile], "## Alpha", "## Alpha two", 1)
	_, out = mustApply(t, f, func(o *ApplyOptions) { o.DryRun = true })
	if len(f.writes) != 0 || !strings.Contains(out, "update #3 (a): plan text changed") || !strings.Contains(out, "update the Implementation Epic #7") {
		t.Errorf("dry run of a change wrote %v:\n%s", f.writes, out)
	}
}

func TestApplyAmend(t *testing.T) {
	f := withPlan(plan1)
	mustApply(t, f, nil)
	// a #3, b #4, c #5, d #6, epic #7; #8 is the owner's own issue, which
	// they made a block a by hand.
	f.add(&ForgeIssue{Title: "Owner's", Poster: owner})
	f.deps[3] = append(f.deps[3], 8)
	// b is edited in Forgejo, c is closed.
	f.issues[4].Body = strings.Replace(f.issues[4].Body, "Body of b.", "Body of b, by hand.", 1)
	f.issues[5].Closed = true
	f.writes = nil

	// a, b and c change; b no longer depends on #2; d is removed; e is new.
	plan2 := strings.NewReplacer(
		"Body of a.", "Body of a, v2.",
		"Body of b.", "Body of b, v2.",
		`depends = ["a", "#2"]`, `depends = ["a"]`,
		"Body of c.", "Body of c, v2.",
		"## Delta\n\n'''issue\nkey = \"d\"\ndepends = [\"c\"]\n'''\n\nBody of d.\n",
		"## Echo\n\n'''issue\nkey = \"e\"\ndepends = [\"a\"]\n'''\n\nBody of e.\n",
	).Replace(plan1)
	f.files[planFile] = string(md(plan2))
	rep, out := mustApply(t, f, nil)

	if want := []string{"edit #3", "create #9", "edit #7", "dep #4-#2", "dep #7-#6", "dep #9+#3", "dep #7+#9"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %v, want %v\n%s", f.writes, want, out)
	}
	for _, s := range []string{
		"#4 (b): modified in Forgejo since last apply",
		"#5 (c): issue is closed; plan text change skipped",
		"#6 (d): removed from the plan; left open",
	} {
		if !slices.ContainsFunc(rep.Flags, func(f string) bool { return strings.Contains(f, s) }) {
			t.Errorf("flags lack %q: %v", s, rep.Flags)
		}
	}
	if !strings.Contains(f.issues[3].Body, "Body of a, v2.") || strings.Contains(f.issues[4].Body, "v2") || strings.Contains(f.issues[5].Body, "v2") {
		t.Errorf("bodies: a %q, b %q, c %q", f.issues[3].Body, f.issues[4].Body, f.issues[5].Body)
	}
	if f.issues[6].Closed || !slices.Equal(f.deps[6], []int64{5}) {
		t.Errorf("removed d: closed %v, blocked by %v", f.issues[6].Closed, f.deps[6])
	}
	if !slices.Equal(f.deps[3], []int64{8}) || !slices.Equal(f.deps[7], []int64{3, 4, 5, 9}) {
		t.Errorf("a blocked by %v (want the owner's #8 kept), epic by %v", f.deps[3], f.deps[7])
	}
	epic := f.issues[7].Body
	for _, s := range []string{
		"- [ ] #4 — Bravo (`b`, depends on #3)\n- [x] #5 — Charlie (`c`, depends on #4)\n- [ ] #9 — Echo (`e`, depends on #3)\n",
		"### Flagged (removed from plan)\n\n- #6 — Delta (`d`, open): no longer in the plan\n",
	} {
		if !strings.Contains(epic, s) {
			t.Errorf("epic lacks %q:\n%s", s, epic)
		}
	}

	// --force overwrites the hand edit, and only that is left to do.
	f.writes = nil
	mustApply(t, f, func(o *ApplyOptions) { o.Force = true })
	if !slices.Equal(f.writes, []string{"edit #4"}) || !strings.Contains(f.issues[4].Body, "Body of b, v2.") {
		t.Errorf("--force wrote %v; b is %q", f.writes, f.issues[4].Body)
	}
	f.writes = nil
	if rep, _ := mustApply(t, f, nil); len(f.writes) != 0 || len(rep.Flags) != 2 {
		t.Errorf("after --force: writes %v, flags %v", f.writes, rep.Flags)
	}
}

func TestApplyResumes(t *testing.T) {
	f := withPlan(plan1)
	f.failCreate = 3 // a and b are created, then c fails
	rep, out, err := apply(t, f, nil)
	if err == nil || !strings.Contains(err.Error(), "injected failure") || !strings.Contains(err.Error(), "run apply again") || rep.Writes != 2 {
		t.Fatalf("interrupted apply: %v %+v\n%s", err, rep, out)
	}
	f.failCreate = 0
	mustApply(t, f, nil)
	got := f.managed("demo")
	for _, k := range []string{"a", "b", "c", "d", ""} {
		if len(got[k]) != 1 {
			t.Errorf("key %q on %v", k, got[k])
		}
	}
	if !slices.Equal(f.deps[4], []int64{3, 2}) || !slices.Equal(f.deps[7], []int64{3, 4, 5, 6}) {
		t.Errorf("deps after resume: %v", f.deps)
	}
	f.writes = nil
	if mustApply(t, f, nil); len(f.writes) != 0 {
		t.Errorf("apply after resume wrote %v", f.writes)
	}
}

func TestApplySpoofedMarkerIgnored(t *testing.T) {
	f := withPlan(plan1)
	// agy files an issue with a's marker, in another milestone.
	spoof := withMarker("evil", Marker{Plan: "demo", Key: "a", SHA: Hash("Alpha", "evil")})
	f.add(&ForgeIssue{Title: "Alpha", Body: spoof, Poster: "agy"}) // #3
	rep, _ := mustApply(t, f, nil)
	if got := f.managed("demo"); !slices.Equal(got["a"], []int64{4}) {
		t.Errorf("a is %v, want a new issue #4", got["a"])
	}
	if len(rep.Flags) != 1 || !strings.Contains(rep.Flags[0], "#3: ignored its marker for plan \"demo\": the issue is by agy") {
		t.Errorf("flags: %v", rep.Flags)
	}
	if f.issues[3].Body != spoof || len(f.deps[3]) != 0 {
		t.Error("apply touched the spoofed issue")
	}
}

func TestApplyRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeForge) // after a first apply when applied
		apply bool
		want  string
	}{
		{"no plan file", func(f *fakeForge) { delete(f.files, planFile) }, false, "docs/milestones/demo.md is not on main at 111111111111"},
		{"plan does not check", func(f *fakeForge) {
			f.files[planFile] = strings.Replace(f.files[planFile], `depends = ["b"]`, `depends = ["zz"]`, 1)
		}, false, `dependency "zz" is not a key`},
		{"milestones_dir", func(f *fakeForge) { f.files[".bottown.toml"] = "milestones_dir = \"plans\"\n" }, false, "plans/demo.md is not on main"},
		{"no kickoff", func(f *fakeForge) { delete(f.issues, 1) }, false, "kickoff #1 does not exist"},
		{"kickoff is a PR", func(f *fakeForge) { f.issues[1].PR = true }, false, "kickoff #1 is a pull request"},
		{"kickoff has no milestone", func(f *fakeForge) { f.issues[1].Milestone = nil }, false, "kickoff #1 has no milestone"},
		{"milestone closed", func(f *fakeForge) { f.milestones[7].Closed = true }, false, `kickoff #1's milestone "M1" is closed`},
		{"no design", func(f *fakeForge) { delete(f.files, "docs/d.md") }, false, `design "docs/d.md" is not a file on main`},
		{"external missing", func(f *fakeForge) { delete(f.issues, 2) }, false, "b: dependency #2 does not exist"},
		{"external is a PR", func(f *fakeForge) { f.issues[2].PR = true }, false, "b: dependency #2 is a pull request"},
		{"external is the epic", func(f *fakeForge) {
			f.files[planFile] = strings.Replace(f.files[planFile], `"#2"`, `"#7"`, 1)
		}, true, "b: dependency #7 is the plan's Implementation Epic"},
		{"duplicate key", func(f *fakeForge) {
			f.add(&ForgeIssue{Title: "Copy", Body: f.issues[4].Body, Poster: owner, Milestone: f.milestones[7]})
		}, true, `#4 and #8 both carry the marker for key "b" of plan "demo"`},
		{"duplicate epic", func(f *fakeForge) {
			f.add(&ForgeIssue{Title: "Copy", Body: f.issues[7].Body, Poster: owner, Milestone: f.milestones[7]})
		}, true, `#7 and #8 both carry the marker for the epic of plan "demo"`},
		{"managed issue in another milestone", func(f *fakeForge) {
			f.milestones[9] = &ForgeMilestone{ID: 9, Title: "M2"}
			f.issues[5].Milestone = f.milestones[9]
		}, true, `#5 (key "c" of plan "demo") is in milestone "M2", not kickoff #1's milestone "M1"`},
		{"cycle through the forge", func(f *fakeForge) {
			// The owner made #2 blocked by d, and b depends on #2.
			f.deps[2] = []int64{6}
		}, true, "cycle with the forge's: #4 (b) -> #2 -> #6 (d) -> #5 (c) -> #4 (b)"},
		{"a cycle among other issues", func(f *fakeForge) {
			// #2 and #3 block each other: not the plan's cycle.
			f.add(&ForgeIssue{Title: "Other", Poster: owner}) // #3
			f.deps[2], f.deps[3] = []int64{3}, []int64{2}
		}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := withPlan(plan1)
			if tc.apply {
				mustApply(t, f, nil)
			}
			tc.setup(f)
			f.writes = nil
			_, out, err := apply(t, f, nil)
			if tc.want == "" {
				// A cycle among other issues alone is not the plan's.
				if err != nil {
					t.Errorf("apply: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("apply: %v, want %q\n%s", err, tc.want, out)
			}
			if len(f.writes) != 0 {
				t.Errorf("a refused apply wrote %v", f.writes)
			}
		})
	}
}

func TestMarker(t *testing.T) {
	for _, m := range []Marker{
		{Plan: "demo", Key: "plan-format", SHA: "0123456789abcdef"},
		{Plan: "demo", Epic: true, SHA: "fedcba9876543210"},
	} {
		body := withMarker("Some text.\r\n\r\nMore.\n", m)
		got, content, ok := ParseMarker(body + "\n\n")
		if !ok || got != m || content != "Some text.\n\nMore." {
			t.Errorf("ParseMarker(%q) = %+v %q %v", body, got, content, ok)
		}
	}
	for _, body := range []string{
		"",
		"text",
		`<!-- bottown:milestone v=2 plan="demo" key="a" sha="0123456789abcdef" -->`,
		`<!-- bottown:milestone v=1 plan="demo" key="a" sha="0123" -->`,
		"<!-- bottown:milestone v=1 plan=\"demo\" key=\"a\" sha=\"0123456789abcdef\" -->\nafter",
	} {
		if m, _, ok := ParseMarker(body); ok {
			t.Errorf("ParseMarker(%q) = %+v", body, m)
		}
	}
	if Hash("T", "body\r\n") != Hash("T", "body") || Hash("T", "a") == Hash("T", "b") || len(Hash("T", "")) != 16 {
		t.Error("Hash")
	}
	if Trusted("agy", owner) || !Trusted(owner, owner) || Trusted("", "") {
		t.Error("Trusted")
	}
}
