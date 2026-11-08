package land

import (
	"slices"
	"testing"
)

func prNumbers(cs []Candidate) []int64 {
	if cs == nil {
		return nil
	}
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.PR
	}
	return out
}

func TestOrder(t *testing.T) {
	tests := []struct {
		name         string
		candidates   []Candidate
		wantOrder    []int64
		wantOverlaps map[int64]Overlap
	}{
		{
			name:         "no candidates",
			candidates:   nil,
			wantOrder:    nil,
			wantOverlaps: map[int64]Overlap{},
		},
		{
			name: "single candidate",
			candidates: []Candidate{
				{PR: 42, Clean: true, Files: []string{"a.go", "b.go"}},
			},
			wantOrder: []int64{42},
			wantOverlaps: map[int64]Overlap{
				42: {PR: 42, Count: 0, Files: nil},
			},
		},
		{
			name: "clean first",
			candidates: []Candidate{
				{PR: 10, Clean: false, Files: []string{"a.go"}},
				{PR: 20, Clean: true, Files: []string{"b.go", "c.go"}},
				{PR: 15, Clean: true, Files: []string{"b.go"}},
				{PR: 5, Clean: false, Files: []string{"d.go", "e.go"}},
			},
			wantOrder: []int64{15, 20, 5, 10},
			wantOverlaps: map[int64]Overlap{
				10: {PR: 10, Count: 0, Files: nil},
				20: {PR: 20, Count: 1, Files: []string{"b.go"}},
				15: {PR: 15, Count: 1, Files: []string{"b.go"}},
				5:  {PR: 5, Count: 0, Files: nil},
			},
		},
		{
			name: "broad PR overlapping three narrow ones goes last",
			candidates: []Candidate{
				{PR: 1, Clean: true, Files: []string{"a.go", "b.go", "c.go", "wide.go"}},
				{PR: 2, Clean: true, Files: []string{"a.go"}},
				{PR: 3, Clean: true, Files: []string{"b.go"}},
				{PR: 4, Clean: true, Files: []string{"c.go"}},
			},
			wantOrder: []int64{2, 3, 4, 1},
			wantOverlaps: map[int64]Overlap{
				1: {PR: 1, Count: 3, Files: []string{"a.go", "b.go", "c.go"}},
				2: {PR: 2, Count: 1, Files: []string{"a.go"}},
				3: {PR: 3, Count: 1, Files: []string{"b.go"}},
				4: {PR: 4, Count: 1, Files: []string{"c.go"}},
			},
		},
		{
			name: "ties by number",
			candidates: []Candidate{
				{PR: 30, Clean: true, Files: []string{"x.go"}},
				{PR: 10, Clean: true, Files: []string{"y.go"}},
				{PR: 20, Clean: true, Files: []string{"z.go"}},
				{PR: 25, Clean: false, Files: []string{"x.go"}},
				{PR: 15, Clean: false, Files: []string{"y.go"}},
				{PR: 35, Clean: false, Files: []string{"z.go"}},
			},
			wantOrder: []int64{10, 20, 30, 15, 25, 35},
			wantOverlaps: map[int64]Overlap{
				30: {PR: 30, Count: 1, Files: []string{"x.go"}},
				10: {PR: 10, Count: 1, Files: []string{"y.go"}},
				20: {PR: 20, Count: 1, Files: []string{"z.go"}},
				25: {PR: 25, Count: 1, Files: []string{"x.go"}},
				15: {PR: 15, Count: 1, Files: []string{"y.go"}},
				35: {PR: 35, Count: 1, Files: []string{"z.go"}},
			},
		},
		{
			name: "renames count both paths",
			candidates: []Candidate{
				// PR 10 renamed old.go -> new.go and also touched common.go;
				// it overlaps PR 20 on old.go and PR 30 on new.go (2 other candidates).
				{PR: 10, Clean: true, Files: []string{"old.go", "new.go", "common.go"}},
				{PR: 20, Clean: true, Files: []string{"old.go"}},
				{PR: 30, Clean: true, Files: []string{"new.go"}},
			},
			wantOrder: []int64{20, 30, 10},
			wantOverlaps: map[int64]Overlap{
				10: {PR: 10, Count: 2, Files: []string{"new.go", "old.go"}},
				20: {PR: 20, Count: 1, Files: []string{"old.go"}},
				30: {PR: 30, Count: 1, Files: []string{"new.go"}},
			},
		},
		{
			name: "multiple shared files with one candidate count as one candidate overlap",
			candidates: []Candidate{
				{PR: 1, Clean: true, Files: []string{"a.go", "b.go", "c.go"}},
				{PR: 2, Clean: true, Files: []string{"a.go", "b.go", "c.go"}},
				{PR: 3, Clean: true, Files: []string{"d.go", "e.go"}},
				{PR: 4, Clean: true, Files: []string{"d.go"}},
				{PR: 5, Clean: true, Files: []string{"e.go"}},
			},
			// PR 1 and 2 each share 3 files with 1 other candidate (count=1).
			// PR 3 shares 1 file each with PR 4 and PR 5 (count=2).
			// PR 4 and 5 each share 1 file with PR 3 (count=1).
			wantOrder: []int64{1, 2, 4, 5, 3},
			wantOverlaps: map[int64]Overlap{
				1: {PR: 1, Count: 1, Files: []string{"a.go", "b.go", "c.go"}},
				2: {PR: 2, Count: 1, Files: []string{"a.go", "b.go", "c.go"}},
				3: {PR: 3, Count: 2, Files: []string{"d.go", "e.go"}},
				4: {PR: 4, Count: 1, Files: []string{"d.go"}},
				5: {PR: 5, Count: 1, Files: []string{"e.go"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := slices.Clone(tt.candidates)
			got := Order(tt.candidates)
			if !slices.Equal(prNumbers(got), tt.wantOrder) {
				t.Errorf("Order() PRs = %v, want %v", prNumbers(got), tt.wantOrder)
			}
			// Input slice must not be mutated.
			if !slices.Equal(prNumbers(tt.candidates), prNumbers(orig)) {
				t.Errorf("Order() mutated input slice: got %v, want %v", prNumbers(tt.candidates), prNumbers(orig))
			}
			overlaps := Overlaps(tt.candidates)
			if len(overlaps) != len(tt.wantOverlaps) {
				t.Fatalf("Overlaps() len = %d, want %d", len(overlaps), len(tt.wantOverlaps))
			}
			for pr, want := range tt.wantOverlaps {
				gotOv, ok := overlaps[pr]
				if !ok {
					t.Errorf("Overlaps() missing PR %d", pr)
					continue
				}
				if gotOv.PR != want.PR || gotOv.Count != want.Count || !slices.Equal(gotOv.Files, want.Files) {
					t.Errorf("Overlaps()[%d] = %+v, want %+v", pr, gotOv, want)
				}
			}
		})
	}
}
