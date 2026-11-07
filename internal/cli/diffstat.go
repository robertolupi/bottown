package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/robertolupi/bottown/internal/diffstat"
)

// Rendering of a pull request's unified diff for `pr show`: a per-file
// summary first, then the diff with lock files collapsed. Collapsing is
// always announced, so a reviewer knows what was not shown. The parsing
// itself is internal/diffstat, which the statistics' measured size reads
// too.

// writeDiffStat prints a summary like `git diff --stat`.
func writeDiffStat(out io.Writer, files []diffstat.File) {
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
func writeDiff(out io.Writer, files []diffstat.File) {
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
