// Package land orders and lands approved pull requests.
package land

import (
	"cmp"
	"slices"
)

// Candidate is an open pull request considered for landing, with the result of
// its trial merge against the current base and the files its delta touches
// (design §5.2).
type Candidate struct {
	PR    int64
	Clean bool
	Files []string
}

// Overlap is a candidate's file overlap with the other candidates in the batch,
// for the dry run's report (design §5.2, §5.4): Count is the number of other
// candidates that share at least one touched file with PR, and Files are the
// sorted unique paths it shares with at least one other candidate.
type Overlap struct {
	PR    int64
	Count int
	Files []string
}

// Order returns a copy of candidates ordered for landing (design §5.2):
//  1. candidates whose trial merge with the current base is clean, before those
//     that conflict;
//  2. among them, the fewest other candidates in the batch that share a touched
//     file, ascending;
//  3. then the lowest PR number.
//
// The caller recomputes the order before each landing with fresh trial merges
// against the remaining candidates.
func Order(candidates []Candidate) []Candidate {
	if len(candidates) == 0 {
		return nil
	}
	overlaps := Overlaps(candidates)
	out := slices.Clone(candidates)
	for i := range out {
		out[i].Files = slices.Clone(out[i].Files)
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		if a.Clean != b.Clean {
			if a.Clean {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(overlaps[a.PR].Count, overlaps[b.PR].Count); c != 0 {
			return c
		}
		return cmp.Compare(a.PR, b.PR)
	})
	return out
}

// Overlaps computes each candidate's overlap with the other candidates in
// candidates, keyed by PR number: how many other candidates share at least one
// touched file, and the sorted unique files shared.
func Overlaps(candidates []Candidate) map[int64]Overlap {
	out := make(map[int64]Overlap, len(candidates))
	sets := make([]map[string]struct{}, len(candidates))
	for i, c := range candidates {
		s := make(map[string]struct{}, len(c.Files))
		for _, f := range c.Files {
			if f != "" {
				s[f] = struct{}{}
			}
		}
		sets[i] = s
	}
	for i, ci := range candidates {
		var count int
		var shared []string
		seen := make(map[string]struct{})
		for j, cj := range candidates {
			if i == j || ci.PR == cj.PR {
				continue
			}
			hit := false
			for f := range sets[i] {
				if _, ok := sets[j][f]; ok {
					hit = true
					if _, dup := seen[f]; !dup {
						seen[f] = struct{}{}
						shared = append(shared, f)
					}
				}
			}
			if hit {
				count++
			}
		}
		slices.Sort(shared)
		out[ci.PR] = Overlap{
			PR:    ci.PR,
			Count: count,
			Files: shared,
		}
	}
	return out
}
