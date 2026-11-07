package diffstat

import "testing"

const sample = `diff --git a/go.sum b/go.sum
@@ -1 +1,2 @@
 a
+b
diff --git a/a.go b/a.go
@@ -1,2 +1,3 @@
-a
-b
+c
+d
+e
diff --git a/logo.png b/logo.png
Binary files /dev/null and b/logo.png differ
`

func TestParseAndSum(t *testing.T) {
	files := Parse(sample)
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(files), files)
	}
	if files[0].Path != "go.sum" || !files[0].Collapsed || files[0].Added != 1 {
		t.Errorf("go.sum = %+v", files[0])
	}
	if files[1].Path != "a.go" || files[1].Added != 3 || files[1].Deleted != 2 {
		t.Errorf("a.go = %+v", files[1])
	}
	if !files[2].Binary {
		t.Errorf("logo.png = %+v", files[2])
	}
	// The lock file is excluded from the measured size; the binary still
	// counts as a file touched.
	s := Sum(files)
	if s.Added != 3 || s.Deleted != 2 || s.Files != 2 {
		t.Errorf("Sum = %+v, want +3 -2, 2 files", s)
	}
}
