package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func TestAttachmentFile(t *testing.T) {
	for _, tc := range []struct {
		id   int64
		name string
		want string
	}{
		{1, "screenshot.png", "1-screenshot.png"},
		{2, "../../etc/passwd", "2-passwd"},
		{3, `C:\Users\me\shot 1.png`, "3-shot_1.png"},
		{4, ".hidden", "4-hidden"},
		{5, "..", "5-attachment"},
		{6, "", "6-attachment"},
		{7, "résumé (final).pdf", "7-r_sum_final_.pdf"},
		{8, strings.Repeat("a", 300) + ".png", "8-" + strings.Repeat("a", 96) + ".png"},
	} {
		if got := AttachmentFile(tc.id, tc.name); got != tc.want {
			t.Errorf("AttachmentFile(%d, %q) = %q, want %q", tc.id, tc.name, got, tc.want)
		}
	}
	// Same names, distinct paths.
	if AttachmentFile(10, "image.png") == AttachmentFile(11, "image.png") {
		t.Error("duplicate names share a path")
	}
}

func TestFetchAttachments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r-1.attachments")
	files := map[int64][]byte{1: png, 2: []byte("hello\n"), 4: png}
	var opened []int64
	open := func(_ context.Context, id int64) (io.ReadCloser, string, error) {
		opened = append(opened, id)
		b, ok := files[id]
		if !ok {
			return nil, "", errors.New("404 Not Found")
		}
		ct := map[int64]string{1: "image/png", 2: "application/octet-stream"}[id]
		return io.NopCloser(bytes.NewReader(b)), ct, nil
	}
	list := []Attachment{
		{ID: 1, Name: "image.png", Size: int64(len(png))},
		{ID: 2, Name: "notes.txt", Size: 6, Comment: 7},
		{ID: 3, Name: "gone.txt", Size: 3},
		{ID: 4, Name: "image.png", Size: int64(len(png)), Comment: 7},
		{ID: 5, Name: "huge.bin", Size: MaxAttachmentBytes + 1},
	}
	got := FetchAttachments(t.Context(), dir, list, open)
	if len(got) != len(list) {
		t.Fatalf("got %d attachments", len(got))
	}
	want := []struct{ path, ct, err string }{
		{"1-image.png", "image/png", ""},
		{"2-notes.txt", "text/plain; charset=utf-8", ""}, // octet-stream: sniffed
		{"", "", "download failed: 404 Not Found"},
		{"4-image.png", "image/png", ""}, // sniffed: the server sent none
		{"", "", "not downloaded: larger than 100.0 MiB"},
	}
	for i, w := range want {
		a := got[i]
		if w.path == "" {
			if a.Path != "" || a.Error != w.err {
				t.Errorf("%d: path %q error %q, want error %q", a.ID, a.Path, a.Error, w.err)
			}
			continue
		}
		if a.Path != filepath.Join(dir, w.path) || a.ContentType != w.ct || a.Error != "" {
			t.Errorf("%d: %+v, want %s %s", a.ID, a, w.path, w.ct)
		}
		if b, err := os.ReadFile(a.Path); err != nil || !bytes.Equal(b, files[a.ID]) {
			t.Errorf("%d: contents %q %v", a.ID, b, err)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", fi, err)
	}
	if fmt.Sprint(opened) != "[1 2 3 4]" {
		t.Errorf("opened %v", opened)
	}
	// No temporary files are left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		t.Errorf("dir has %d entries", len(ents))
	}

	// A second issue show reuses what is there and fetches what is new.
	opened = nil
	files[6] = []byte("new")
	again := FetchAttachments(t.Context(), dir, append(list[:2:2], Attachment{ID: 6, Name: "new.txt", Size: 3}), open)
	if fmt.Sprint(opened) != "[6]" || again[0].ContentType != "image/png" || again[2].Path == "" {
		t.Errorf("second fetch opened %v: %+v", opened, again)
	}

	if err := RemoveAttachments(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dir not removed: %v", err)
	}
	if err := RemoveAttachments(dir); err != nil {
		t.Errorf("removing a missing dir: %v", err)
	}
}

func TestRemoveFilesRemovesAttachments(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	dir := w.AttachmentsDir("r-1")
	if dir != filepath.Join(w.RunsDir, "r-1.attachments") {
		t.Fatalf("AttachmentsDir %s", dir)
	}
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "1-a.png"), png, 0o600)
	w.RemoveFiles("r-1")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("attachments survive RemoveFiles: %v", err)
	}
}

func TestAttachmentsPrompt(t *testing.T) {
	d := PromptData{RunID: "r-1", Actor: "claude", Repo: "o/r", Issue: 7, PR: 8, Title: "T", Body: "See the screenshot.", SHA: "abc", Base: "main", Bottown: "/b"}
	for _, kind := range []string{"implement", "verify", "discuss", "plan"} {
		without, err := Prompt(kind, d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(without, "## Attachments") {
			t.Errorf("%s prompt without attachments has the section:\n%s", kind, without)
		}
		with := d
		with.AttachmentsOf = 7
		with.Attachments = []Attachment{
			{ID: 1, Name: "shot.png", Size: 4096, ContentType: "image/png", Path: "/runs/r-1.attachments/1-shot.png"},
			{ID: 2, Name: "trace.log", Size: 12, Comment: 30, Error: "download failed: 502 Bad Gateway"},
		}
		p, err := Prompt(kind, with)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"See the screenshot.\n\n## Attachments\n\nFiles attached to #7",
			"`/b issue show 7` lists",
			"\n- `/runs/r-1.attachments/1-shot.png`: \"shot.png\" (image/png, 4.0 KiB, on the description)\n",
			"\n- \"trace.log\" (12 bytes, on comment 30): download failed: 502 Bad Gateway\n\n",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks %q:\n%s", kind, want, p)
			}
		}
		with.Attachments = nil
		with.AttachmentsError = "listing attachments: 500"
		if p, _ := Prompt(kind, with); !strings.Contains(p, "- The attachments could not be listed: listing attachments: 500\n") {
			t.Errorf("%s prompt without the listing error:\n%s", kind, p)
		}
	}
}

func TestRedactEncoded(t *testing.T) {
	b64 := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 40) + "=="
	for _, tc := range []struct{ in, want string }{
		{"plain text stays", "plain text stays"},
		{"short base64 aGVsbG8= stays", "short base64 aGVsbG8= stays"},
		{"img " + b64 + " end", "img [962 encoded bytes elided by bottown] end"},
		{`<img src="data:image/png;base64,iVBORw0KGgo=">`, `<img src="[34 encoded bytes elided by bottown]">`},
		{"/a/very/long/path/" + strings.Repeat("dir-name.", 40), "/a/very/long/path/" + strings.Repeat("dir-name.", 40)},
	} {
		if got := string(RedactEncoded([]byte(tc.in))); got != tc.want {
			t.Errorf("RedactEncoded(%.40q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A harness's tool result with an image stays valid JSON.
	line := `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`
	red := RedactEncoded([]byte(line))
	if !json.Valid(red) || strings.Contains(string(red), "iVBOR") {
		t.Errorf("redacted line: %s", red)
	}
}

func TestRedactingWriter(t *testing.T) {
	var out bytes.Buffer
	w := NewRedactingWriter(&out)
	b64 := strings.Repeat("QUFB", 100)
	// The payload arrives split across writes.
	for _, chunk := range []string{"start\n{\"data\":\"", b64[:150], b64[150:], "\"}\npartial"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if strings.Contains(out.String(), "partial") {
		t.Errorf("partial line written early: %q", out.String())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := "start\n{\"data\":\"[400 encoded bytes elided by bottown]\"}\npartial"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestRenderLogElidesEncoded(t *testing.T) {
	b64 := strings.Repeat("QUFB", 100)
	lines := RenderLog("script", []byte("reading the screenshot\n"+b64+"\n"))
	if len(lines) != 2 || lines[0].Text != "reading the screenshot" || lines[1].Text != "[400 encoded bytes elided by bottown]" {
		t.Errorf("lines %q", lines)
	}
}
