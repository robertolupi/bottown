package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// readPushRefs consumes only command pkt-lines, through their flush packet.
// Replay the returned prefix before streaming the remaining pack.
func readPushRefs(r io.Reader) ([]string, []byte, error) {
	var prefix bytes.Buffer
	var refs []string
	for {
		var header [4]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, nil, fmt.Errorf("reading push command: %w", err)
		}
		prefix.Write(header[:])
		n, err := strconv.ParseUint(string(header[:]), 16, 16)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid push pkt-line length")
		}
		if n == 0 {
			return refs, prefix.Bytes(), nil
		}
		if n < 4 || n > 65520 || prefix.Len()+int(n)-4 > 1024*1024 {
			return nil, nil, fmt.Errorf("invalid or oversized push commands")
		}
		payload := make([]byte, int(n)-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, nil, fmt.Errorf("reading push command: %w", err)
		}
		prefix.Write(payload)
		command, _, caps := strings.Cut(string(payload), "\x00")
		if caps && len(refs) != 0 {
			return nil, nil, fmt.Errorf("capabilities after first push command")
		}
		fields := strings.Split(strings.TrimSuffix(command, "\n"), " ")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
			return nil, nil, fmt.Errorf("invalid push ref command")
		}
		refs = append(refs, fields[2])
	}
}

// writePushError uses the client's negotiated framing so git displays the
// refusal even when it is expecting sideband data rather than status packets.
func writePushError(w http.ResponseWriter, prefix []byte, message string) {
	payload := "ERR " + message + "\n"
	if len(prefix) >= 4 {
		n, err := strconv.ParseUint(string(prefix[:4]), 16, 16)
		if err == nil && n >= 4 && int(n) <= len(prefix) {
			_, caps, _ := strings.Cut(string(prefix[4:n]), "\x00")
			if slices.Contains(strings.Fields(caps), "side-band-64k") {
				payload = "\x03" + message + "\n"
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	fmt.Fprintf(w, "%04x%s0000", len(payload)+4, payload)
}
