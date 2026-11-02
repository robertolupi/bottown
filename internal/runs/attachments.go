package runs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Issue attachments as local files. A run's attachments directory belongs
// to the run: bottown fills it when the run starts and again on each
// `issue show` in the run, and removes it when the run ends, when
// `run start` or `run end` recover an interrupted run, and in `run gc`.
// Harnesses open the files with their own tools; the prompt and `issue
// show` carry only paths and metadata, never the contents.

// MaxAttachmentBytes bounds one downloaded attachment.
const MaxAttachmentBytes = 100 << 20

// Attachment is an issue attachment and, once fetched, its local copy.
type Attachment struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Comment     int64  `json:"comment_id,omitempty"` // 0: on the description
	ContentType string `json:"content_type,omitempty"`
	Path        string `json:"path,omitempty"`  // the local file, once downloaded
	Error       string `json:"error,omitempty"` // why it was not downloaded
}

// String is the attachment's line in a prompt or `issue show`.
func (a Attachment) String() string {
	where := "on the description"
	if a.Comment > 0 {
		where = fmt.Sprintf("on comment %d", a.Comment)
	}
	meta := []string{}
	if a.ContentType != "" {
		meta = append(meta, a.ContentType)
	}
	meta = append(meta, humanSize(a.Size), where)
	if a.Path == "" {
		reason := a.Error
		if reason == "" {
			reason = "not downloaded"
		}
		return fmt.Sprintf("%q (%s): %s", a.Name, strings.Join(meta, ", "), reason)
	}
	return fmt.Sprintf("`%s`: %q (%s)", a.Path, a.Name, strings.Join(meta, ", "))
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// AttachmentsDir is where launched run id keeps its attachments, next to
// its other files.
func (w *Workspace) AttachmentsDir(id string) string { return w.Path(id, ".attachments") }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// AttachmentFile is the file name of attachment id: the original name made
// safe, prefixed with the id, so two attachments with the same name get
// distinct paths and no name escapes the directory.
func AttachmentFile(id int64, name string) string {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.Trim(unsafeName.ReplaceAllString(base, "_"), "._")
	if len(base) > 100 {
		ext := filepath.Ext(base)
		if len(ext) > 20 {
			ext = ""
		}
		base = base[:100-len(ext)] + ext
	}
	if base == "" {
		base = "attachment"
	}
	return fmt.Sprintf("%d-%s", id, base)
}

// OpenAttachment opens attachment id's contents and says its content type.
type OpenAttachment func(ctx context.Context, id int64) (io.ReadCloser, string, error)

// FetchAttachments downloads each attachment into dir (created 0700) and
// returns them with their paths, or with the error that stopped each one:
// one failure does not stop the others. A file already there with the
// listed size is not downloaded again.
func FetchAttachments(ctx context.Context, dir string, list []Attachment, open OpenAttachment) []Attachment {
	out := make([]Attachment, len(list))
	mkErr := os.MkdirAll(dir, 0o700)
	for i, a := range list {
		if mkErr != nil {
			a.Error = mkErr.Error()
		} else if err := fetchOne(ctx, dir, &a, open); err != nil {
			a.Error = err.Error()
		}
		out[i] = a
	}
	return out
}

func fetchOne(ctx context.Context, dir string, a *Attachment, open OpenAttachment) error {
	if a.Size > MaxAttachmentBytes {
		return fmt.Errorf("not downloaded: larger than %s", humanSize(MaxAttachmentBytes))
	}
	path := filepath.Join(dir, AttachmentFile(a.ID, a.Name))
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Size() == a.Size {
		a.Path, a.ContentType = path, sniff(path)
		return nil
	}
	body, ct, err := open(ctx, a.ID)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer body.Close()
	f, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, io.LimitReader(body, MaxAttachmentBytes+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil:
		return fmt.Errorf("download failed: %w", err)
	case n > MaxAttachmentBytes:
		return fmt.Errorf("not downloaded: larger than %s", humanSize(MaxAttachmentBytes))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	a.Path, a.Size = path, n
	a.ContentType = ct
	if a.ContentType == "" || strings.HasPrefix(a.ContentType, "application/octet-stream") {
		a.ContentType = sniff(path)
	}
	return nil
}

// sniff guesses a local file's content type from its first bytes.
func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 512)
	n, _ := io.ReadFull(f, b)
	return http.DetectContentType(b[:n])
}

// RemoveAttachments deletes an attachments directory; a missing one is fine.
func RemoveAttachments(dir string) error {
	if dir == "" {
		return nil
	}
	err := os.RemoveAll(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Encoded payloads are elided from logs and the Live Runs tail: a data URL,
// or a run of base64 too long to be anything else. A harness that opens an
// image attachment logs it base64-encoded in its tool result.
var encoded = regexp.MustCompile(`data:[A-Za-z0-9.+/-]*(;[A-Za-z0-9.=+-]+)*;base64,[A-Za-z0-9+/]*={0,2}|[A-Za-z0-9+/]{256,}={0,2}`)

// RedactEncoded replaces encoded payloads in b with a short note of their
// length. The note has no quotes or backslashes, so JSON stays valid.
func RedactEncoded(b []byte) []byte {
	if len(b) < 256 && !bytes.Contains(b, []byte(";base64,")) {
		return b
	}
	return encoded.ReplaceAllFunc(b, func(m []byte) []byte {
		return fmt.Appendf(nil, "[%d encoded bytes elided by bottown]", len(m))
	})
}

// RedactingWriter writes lines to w with RedactEncoded applied; Close
// writes a last line without a newline.
type RedactingWriter struct {
	w   io.Writer
	buf []byte
}

// NewRedactingWriter returns a RedactingWriter writing to w.
func NewRedactingWriter(w io.Writer) *RedactingWriter { return &RedactingWriter{w: w} }

func (r *RedactingWriter) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	i := bytes.LastIndexByte(r.buf, '\n')
	if i < 0 {
		return len(p), nil
	}
	if _, err := r.w.Write(RedactEncoded(r.buf[:i+1])); err != nil {
		return 0, err
	}
	r.buf = append(r.buf[:0], r.buf[i+1:]...)
	return len(p), nil
}

// Close flushes what is left; it does not close the underlying writer.
func (r *RedactingWriter) Close() error {
	if len(r.buf) == 0 {
		return nil
	}
	_, err := r.w.Write(RedactEncoded(r.buf))
	r.buf = nil
	return err
}
