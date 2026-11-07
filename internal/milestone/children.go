package milestone

import (
	"context"
	"fmt"
	"slices"
)

// Reader is what Epic needs of the forge: Forge's reads of issues and
// their dependencies.
type Reader interface {
	// Issues lists every issue, open and closed, and no pull requests.
	Issues(ctx context.Context) ([]*ForgeIssue, error)
	// BlockedBy lists the issues n depends on (Forgejo's dependencies).
	BlockedBy(ctx context.Context, n int64) ([]int64, error)
}

// An EpicState is a plan's Implementation Epic and its active children.
type EpicState struct {
	Epic *ForgeIssue
	// Children are the epic's active children, in issue order: the issues
	// that carry a trusted child marker for the plan and are in the epic's
	// current blocked_by set, open or closed. A key removed from the plan
	// is not active: apply leaves its issue and marker in place and drops
	// only its edge from the epic (docs/design/milestones.md §2.6).
	Children []*ForgeIssue
}

// Open is the active children that are still open.
func (e *EpicState) Open() []*ForgeIssue {
	var out []*ForgeIssue
	for _, c := range e.Children {
		if !c.Closed {
			out = append(out, c)
		}
	}
	return out
}

// Epic finds plan slug's Implementation Epic, the owner's issue with its
// epic marker, and the epic's active children. It is ErrNotFound when apply
// filed no epic for the plan, and an error when two issues carry its
// marker.
func Epic(ctx context.Context, r Reader, owner, slug string) (*EpicState, error) {
	issues, err := r.Issues(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing issues: %w", err)
	}
	children := map[int64]*ForgeIssue{}
	st := &EpicState{}
	for _, is := range issues {
		if is.PR || !Trusted(is.Poster, owner) {
			continue
		}
		m, _, ok := ParseMarker(is.Body)
		if !ok || m.Plan != slug {
			continue
		}
		if !m.Epic {
			children[is.Number] = is
			continue
		}
		if st.Epic != nil {
			lo, hi := min(st.Epic.Number, is.Number), max(st.Epic.Number, is.Number)
			return nil, fmt.Errorf("#%d and #%d both carry the epic marker of plan %q: fix the markers by hand", lo, hi, slug)
		}
		st.Epic = is
	}
	if st.Epic == nil {
		return nil, fmt.Errorf("%w: plan %q has no Implementation Epic filed by `bottown milestone apply`", ErrNotFound, slug)
	}
	deps, err := r.BlockedBy(ctx, st.Epic.Number)
	if err != nil {
		return nil, fmt.Errorf("fetching epic #%d's dependencies: %w", st.Epic.Number, err)
	}
	slices.Sort(deps)
	for _, n := range slices.Compact(deps) {
		if c, ok := children[n]; ok {
			st.Children = append(st.Children, c)
		}
	}
	return st, nil
}
