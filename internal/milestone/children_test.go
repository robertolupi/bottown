package milestone

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func numbers(issues []*ForgeIssue) []int64 {
	out := []int64{}
	for _, is := range issues {
		out = append(out, is.Number)
	}
	return out
}

// The active children are the trusted children in the epic's blocked_by:
// a key removed from the plan (apply, remove it, apply again) keeps its
// issue and marker but loses its edge, and so is not active, open or not.
func TestEpicActiveChildren(t *testing.T) {
	ctx := context.Background()
	f := withPlan(plan1)
	if _, err := Epic(ctx, f, owner, "demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before apply: %v, want ErrNotFound", err)
	}
	mustApply(t, f, nil) // a #3, b #4, c #5, d #6, epic #7
	st, err := Epic(ctx, f, owner, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Epic.Number != 7 || !slices.Equal(numbers(st.Children), []int64{3, 4, 5, 6}) {
		t.Fatalf("epic #%d, children %v", st.Epic.Number, numbers(st.Children))
	}

	// An actor's issue with a copied child marker, and the owner's own
	// issue without one, both made to block the epic by hand: neither is
	// a child.
	copied := f.add(&ForgeIssue{Title: "Copy", Poster: "agy", Body: f.issues[3].Body})
	plain := f.add(&ForgeIssue{Title: "Owner's", Poster: owner})
	f.deps[7] = append(f.deps[7], copied, plain)

	f.files[planFile] = string(md(strings.Replace(plan1,
		"## Delta\n\n'''issue\nkey = \"d\"\ndepends = [\"c\"]\n'''\n\nBody of d.\n", "", 1)))
	mustApply(t, f, nil)
	if f.issues[6].Closed {
		t.Fatal("apply closed the removed key's issue")
	}
	if st, err = Epic(ctx, f, owner, "demo"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(numbers(st.Children), []int64{3, 4, 5}) {
		t.Fatalf("after removing d: children %v, want #3 #4 #5", numbers(st.Children))
	}
	if !slices.Equal(numbers(st.Open()), []int64{3, 4, 5}) {
		t.Errorf("open %v", numbers(st.Open()))
	}
	// The hand-linked issues are ignored; d's issue is flagged removed.
	if !slices.Equal(numbers(st.Ignored), []int64{copied, plain}) || !slices.Equal(numbers(st.Removed), []int64{6}) {
		t.Errorf("ignored %v, removed %v", numbers(st.Ignored), numbers(st.Removed))
	}
	if is := st.Issue(2); is == nil || is.Title != "External" {
		t.Errorf("Issue(2) = %+v", is)
	}
	for _, n := range []int64{3, 4, 5} {
		f.issues[n].Closed = true
	}
	if st, err = Epic(ctx, f, owner, "demo"); err != nil {
		t.Fatal(err)
	}
	if open := st.Open(); len(open) != 0 {
		t.Errorf("open %v with every active child closed and the removed #6 open", numbers(open))
	}
}

func TestEpicDuplicateMarker(t *testing.T) {
	f := withPlan(plan1)
	mustApply(t, f, nil)
	f.add(&ForgeIssue{Title: "Again", Poster: owner, Body: f.issues[7].Body})
	if _, err := Epic(context.Background(), f, owner, "demo"); err == nil || !strings.Contains(err.Error(), "#7 and #8") {
		t.Errorf("err = %v, want both epics named", err)
	}
}
