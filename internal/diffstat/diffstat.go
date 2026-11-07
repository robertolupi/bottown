// Package diffstat parses a unified diff into its files: each file's path,
// added and deleted line counts, and whether it is binary or a lock file
// whose content is collapsed. `pr show` and the statistics' measured size
// both read a diff through Parse, so they count the same lines.
package diffstat

import (
	"path"
	"slices"
	"strings"
)

// lockFiles are generated files whose diffs reviewers skim past; they are
// marked Collapsed, and the statistics exclude them from a PR's measured
// size.
var lockFiles = []string{
	"go.sum", "go.work.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
	"Cargo.lock", "poetry.lock", "uv.lock", "Gemfile.lock", "composer.lock",
}

// IsLockFile reports whether p is a generated lock file.
func IsLockFile(p string) bool { return slices.Contains(lockFiles, path.Base(p)) }

// File is one file's section of a unified diff.
type File struct {
	Path      string
	Added     int
	Deleted   int
	Binary    bool
	Collapsed bool
	Text      string // the section, from its "diff --git" line
	// headerPath is from "diff --git a/X b/Y", the fallback for Path.
	headerPath string
}

// Parse splits a git unified diff into files. Text before the first
// "diff --git" line is dropped.
func Parse(diff string) []File {
	var files []File
	var cur *File
	var text strings.Builder
	inHunk := false
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = text.String()
		if cur.Path == "" {
			cur.Path = cur.headerPath
		}
		cur.Collapsed = IsLockFile(cur.Path)
		files = append(files, *cur)
		text.Reset()
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		if line == "" {
			continue
		}
		l := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(l, "diff --git ") {
			flush()
			cur, inHunk = &File{}, false
			if _, b, ok := strings.Cut(l, " b/"); ok {
				cur.headerPath = b
			}
		}
		if cur == nil {
			continue
		}
		text.WriteString(line)
		switch {
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(l, "+"):
			cur.Added++
		case inHunk && strings.HasPrefix(l, "-"):
			cur.Deleted++
		case inHunk:
		case strings.HasPrefix(l, "+++ ") && l != "+++ /dev/null":
			cur.Path = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "--- ") && l != "--- /dev/null" && cur.Path == "":
			cur.Path = strings.TrimPrefix(strings.TrimPrefix(l, "--- "), "a/")
		case strings.HasPrefix(l, "rename to "):
			cur.Path = strings.TrimPrefix(l, "rename to ")
		case strings.HasPrefix(l, "Binary files "):
			cur.Binary = true
		}
	}
	flush()
	return files
}

// Size is a diff's measured size: the added and deleted lines and the files
// touched, lock files excluded.
type Size struct {
	Added   int
	Deleted int
	Files   int
}

// Sum is the measured size of a parsed diff, lock files excluded.
func Sum(files []File) Size {
	var s Size
	for _, f := range files {
		if f.Collapsed {
			continue
		}
		s.Added += f.Added
		s.Deleted += f.Deleted
		s.Files++
	}
	return s
}
