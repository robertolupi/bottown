package cli

import (
	"bytes"
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/go.sum b/go.sum
index a76f376..c00c859 100644
--- a/go.sum
+++ b/go.sum
@@ -1,2 +1,3 @@
 a v1 h1:x
+b v1 h1:y
+b v1/go.mod h1:z
-c v1 h1:w
diff --git a/internal/cli/host.go b/internal/cli/host.go
index b2ec2d6..d6eca3c 100644
--- a/internal/cli/host.go
+++ b/internal/cli/host.go
@@ -1,3 +1,3 @@
 package cli
--- not a header: a removed line starting with --
+++ not a header: an added line starting with ++
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 1111111..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/a.md b/b.md
similarity index 100%
rename from a.md
rename to b.md
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..2222222
Binary files /dev/null and b/logo.png differ
`

func TestParseDiff(t *testing.T) {
	files := parseDiff(sampleDiff)
	type want struct {
		path             string
		added, deleted   int
		binary, collapse bool
	}
	wants := []want{
		{"go.sum", 2, 1, false, true},
		{"internal/cli/host.go", 1, 1, false, false},
		{"old.txt", 0, 1, false, false},
		{"b.md", 0, 0, false, false},
		{"logo.png", 0, 0, true, false},
	}
	if len(files) != len(wants) {
		t.Fatalf("got %d files, want %d: %+v", len(files), len(wants), files)
	}
	for i, w := range wants {
		f := files[i]
		if f.Path != w.path || f.Added != w.added || f.Deleted != w.deleted || f.Binary != w.binary || f.Collapsed != w.collapse {
			t.Errorf("file %d = %+v, want %+v", i, f, w)
		}
	}
	// The sections reassemble the whole diff.
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.Text)
	}
	if all.String() != sampleDiff {
		t.Errorf("sections do not reassemble the diff:\n%s", all.String())
	}
}

func TestWriteDiffStat(t *testing.T) {
	var buf bytes.Buffer
	writeDiffStat(&buf, parseDiff(sampleDiff))
	want := strings.Join([]string{
		" go.sum               | 3 ++- (collapsed)",
		" internal/cli/host.go | 2 +-",
		" old.txt              | 1 -",
		" b.md                 | 0",
		" logo.png             | Bin",
		" 5 files changed, 3 insertions(+), 3 deletions(-)",
		"",
	}, "\n")
	if buf.String() != want {
		t.Errorf("stat:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestWriteDiffCollapses(t *testing.T) {
	files := parseDiff(sampleDiff)
	var buf bytes.Buffer
	writeDiff(&buf, files)
	got := buf.String()
	if strings.Contains(got, "b v1 h1:y") {
		t.Errorf("go.sum content shown although collapsed:\n%s", got)
	}
	if !strings.Contains(got, "[go.sum: lock file, +2 -1, collapsed; --full shows it]") {
		t.Errorf("no collapse note:\n%s", got)
	}
	if !strings.Contains(got, "+++ not a header") || !strings.Contains(got, "Binary files") {
		t.Errorf("other files missing:\n%s", got)
	}

	for i := range files {
		files[i].Collapsed = false
	}
	buf.Reset()
	writeDiff(&buf, files)
	if buf.String() != sampleDiff {
		t.Errorf("uncollapsed diff differs from the input:\n%s", buf.String())
	}
}

func TestBar(t *testing.T) {
	for _, tc := range []struct {
		a, d, most int
		want       string
	}{
		{2, 1, 3, "++-"},
		{0, 0, 0, ""},
		{2, 1, 135, "+-"},
		{100, 0, 100, strings.Repeat("+", 40)},
		{1, 1000, 1001, "+" + strings.Repeat("-", 39)},
		{1000, 1, 1001, strings.Repeat("+", 39) + "-"},
		// The bars of !2, scaled to 40 columns as git does.
		{13, 1, 135, "++++-"},
		{43, 0, 135, strings.Repeat("+", 13)},
		{35, 11, 135, "++++++++++----"},
		{135, 0, 135, strings.Repeat("+", 40)},
	} {
		if got := bar(tc.a, tc.d, tc.most); got != tc.want {
			t.Errorf("bar(%d, %d, %d) = %q, want %q", tc.a, tc.d, tc.most, got, tc.want)
		}
	}
}
