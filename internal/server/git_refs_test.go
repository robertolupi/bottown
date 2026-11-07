package server

import (
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestReadPushRefs(t *testing.T) {
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	first := "old new refs/heads/issue/363"
	for _, tc := range []struct {
		name, commands string
		refs           []string
	}{
		{"single", pkt(first) + "0000", []string{"refs/heads/issue/363"}},
		{"capabilities and multiple", pkt(first+"\x00report-status side-band-64k") + pkt("old new refs/tags/v1") + "0000", []string{"refs/heads/issue/363", "refs/tags/v1"}},
		{"empty", "0000", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.commands + "PACK payload")
			refs, prefix, err := readPushRefs(r)
			if err != nil || !reflect.DeepEqual(refs, tc.refs) || string(prefix) != tc.commands {
				t.Fatalf("refs=%v prefix=%q err=%v", refs, prefix, err)
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != "PACK payload" {
				t.Fatalf("consumed pack: %q", rest)
			}
		})
	}
	for _, input := range []string{"", "00", "zzzz", "0001", "0004", "000bold", pkt(first), pkt(first) + "00", "ffff"} {
		if _, _, err := readPushRefs(strings.NewReader(input)); err == nil {
			t.Errorf("accepted malformed/truncated %q", input)
		}
	}
}

func TestWritePushError(t *testing.T) {
	for _, tc := range []struct{ caps, lead string }{
		{"report-status", "ERR "},
		{"report-status side-band-64k", "\x03"},
		{"report-status no-side-band-64k", "ERR "},
	} {
		command := "old new refs/heads/issue/1\x00" + tc.caps
		prefix := []byte(fmt.Sprintf("%04x%s0000", len(command)+4, command))
		w := httptest.NewRecorder()
		writePushError(w, prefix, "push denied")
		payload := tc.lead + "push denied\n"
		want := fmt.Sprintf("%04x%s0000", len(payload)+4, payload)
		if w.Body.String() != want || w.Header().Get("Content-Type") != "application/x-git-receive-pack-result" {
			t.Fatalf("caps=%q: %q", tc.caps, w.Body.String())
		}
	}
}
