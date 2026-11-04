package cli

import (
	"io"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/robertolupi/bottown/internal/runs"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

type tableWriter struct {
	out  io.Writer
	rows [][]string
}

func newTableWriter(out io.Writer) *tableWriter {
	return &tableWriter{out: out}
}

func (tw *tableWriter) Row(cols ...string) {
	tw.rows = append(tw.rows, cols)
}

func (tw *tableWriter) Flush() error {
	if len(tw.rows) == 0 {
		return nil
	}
	colCount := 0
	for _, r := range tw.rows {
		if len(r) > colCount {
			colCount = len(r)
		}
	}
	widths := make([]int, colCount)
	for _, r := range tw.rows {
		for i, c := range r {
			w := len([]rune(stripANSI(c)))
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	for _, r := range tw.rows {
		for i, c := range r {
			if i > 0 {
				io.WriteString(tw.out, "  ")
			}
			io.WriteString(tw.out, c)
			if i < len(r)-1 {
				pad := widths[i] - len([]rune(stripANSI(c)))
				if pad > 0 {
					io.WriteString(tw.out, strings.Repeat(" ", pad))
				}
			}
		}
		io.WriteString(tw.out, "\n")
	}
	return nil
}

type styles struct {
	r *lipgloss.Renderer

	Bold    lipgloss.Style
	Dim     lipgloss.Style
	Success lipgloss.Style
	Danger  lipgloss.Style
	Warning lipgloss.Style
	Info    lipgloss.Style
	Header  lipgloss.Style
	Purple  lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) *styles {
	return &styles{
		r:       r,
		Bold:    r.NewStyle().Bold(true),
		Dim:     r.NewStyle().Faint(true),
		Success: r.NewStyle().Foreground(lipgloss.ANSIColor(2)), // ANSI green
		Danger:  r.NewStyle().Foreground(lipgloss.ANSIColor(1)), // ANSI red
		Warning: r.NewStyle().Foreground(lipgloss.ANSIColor(3)), // ANSI yellow
		Info:    r.NewStyle().Foreground(lipgloss.ANSIColor(6)), // ANSI cyan
		Header:  r.NewStyle().Bold(true),
		Purple:  r.NewStyle().Foreground(lipgloss.ANSIColor(5)), // ANSI magenta
	}
}

func (s *styles) runStatus(status string) string {
	switch status {
	case runs.StatusSucceeded:
		return s.Success.Bold(true).Render(status)
	case runs.StatusFailed, runs.StatusDead:
		return s.Danger.Bold(true).Render(status)
	case runs.StatusRunning:
		return s.Success.Bold(true).Render(status)
	case runs.StatusUnknown:
		return s.Warning.Bold(true).Render(status)
	default:
		return status
	}
}

func (s *styles) keyState(state string) string {
	switch state {
	case "live":
		return s.Success.Bold(true).Render(state)
	case "expired":
		return s.Danger.Render(state)
	case "ended", "gone":
		return s.Dim.Render(state)
	default:
		return state
	}
}

func (s *styles) issueState(state string) string {
	switch state {
	case "open":
		return s.Success.Bold(true).Render(state)
	case "closed":
		return s.Danger.Bold(true).Render(state)
	default:
		return s.Bold.Render(state)
	}
}

func (s *styles) prState(state string) string {
	switch state {
	case "open":
		return s.Success.Bold(true).Render(state)
	case "merged":
		return s.Purple.Bold(true).Render(state)
	case "closed":
		return s.Danger.Bold(true).Render(state)
	default:
		return s.Bold.Render(state)
	}
}

func (s *styles) reviewVerdict(verdict string) string {
	switch verdict {
	case "APPROVED":
		return s.Success.Bold(true).Render(verdict)
	case "REQUEST_CHANGES":
		return s.Danger.Bold(true).Render(verdict)
	case "COMMENT":
		return s.Warning.Bold(true).Render(verdict)
	default:
		if verdict == "" {
			return ""
		}
		return s.Bold.Render(verdict)
	}
}
