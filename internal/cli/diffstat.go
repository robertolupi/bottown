package cli

import (
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// Rendering of a pull request's unified diff for `pr show`: a per-file
// summary first, then the diff with lock files collapsed. Collapsing is
// always announced, so a reviewer knows what was not shown.

// lockFiles are generated files whose diffs reviewers skim past; `pr show
// --diff` collapses them unless --full is given.
var lockFiles = []string{
	"go.sum", "go.work.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
	"Cargo.lock", "poetry.lock", "uv.lock", "Gemfile.lock", "composer.lock",
}

func isLockFile(p string) bool { return slices.Contains(lockFiles, path.Base(p)) }

// fileDiff is one file's section of a unified diff.
type fileDiff struct {
	Path       string
	Added      int
	Deleted    int
	Binary     bool
	Collapsed  bool
	Text       string // the section, from its "diff --git" line
	headerPath string // from "diff --git a/X b/Y", the fallback for Path
}

// parseDiff splits a git unified diff into files. Text before the first
// "diff --git" line is dropped.
func parseDiff(diff string) []fileDiff {
	var files []fileDiff
	var cur *fileDiff
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
		cur.Collapsed = isLockFile(cur.Path)
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
			cur, inHunk = &fileDiff{}, false
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

// writeDiffStat prints a summary like `git diff --stat`.
func writeDiffStat(out io.Writer, files []fileDiff) {
	width, numWidth, added, deleted, most := 0, 1, 0, 0, 0
	for _, f := range files {
		width = max(width, len(f.Path))
		numWidth = max(numWidth, len(fmt.Sprint(f.Added+f.Deleted)))
		most = max(most, f.Added+f.Deleted)
		added += f.Added
		deleted += f.Deleted
	}
	for _, f := range files {
		if f.Binary {
			fmt.Fprintf(out, " %-*s | %*s\n", width, f.Path, numWidth, "Bin")
			continue
		}
		note := ""
		if f.Collapsed {
			note = " (collapsed)"
		}
		line := fmt.Sprintf(" %-*s | %*d %s%s", width, f.Path, numWidth, f.Added+f.Deleted, bar(f.Added, f.Deleted, most), note)
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	plural := func(n int, s string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, s)
		}
		return fmt.Sprintf("%d %ss", n, s)
	}
	fmt.Fprintf(out, " %s changed, %s(+), %s(-)\n", plural(len(files), "file"), plural(added, "insertion"), plural(deleted, "deletion"))
}

// bar is the +/- histogram of writeDiffStat. When the largest file (most
// changed lines) exceeds 40 columns, every bar is scaled by the same factor
// as git's scale_linear does, so each changed side keeps at least one column.
func bar(added, deleted, most int) string {
	const width = 40
	if most > width {
		scale := func(n int) int {
			if n == 0 {
				return 0
			}
			return 1 + n*(width-1)/most
		}
		total := scale(added + deleted)
		if total < 2 && added > 0 && deleted > 0 {
			total = 2
		}
		if added < deleted {
			added = scale(added)
			deleted = total - added
		} else {
			deleted = scale(deleted)
			added = total - deleted
		}
	}
	return strings.Repeat("+", added) + strings.Repeat("-", deleted)
}

// writeDiff prints the files' diffs, replacing collapsed ones with a note.
func writeDiff(out io.Writer, files []fileDiff) {
	for _, f := range files {
		if f.Collapsed {
			fmt.Fprintf(out, "diff --git a/%s b/%s\n[%s: lock file, +%d -%d, collapsed; --full shows it]\n", f.Path, f.Path, f.Path, f.Added, f.Deleted)
			continue
		}
		io.WriteString(out, f.Text)
		if !strings.HasSuffix(f.Text, "\n") {
			io.WriteString(out, "\n")
		}
	}
}
