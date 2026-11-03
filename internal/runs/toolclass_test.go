package runs

import "testing"

// One case per row of toolClasses, plus the shell heuristic and the
// fallback.
func TestToolClass(t *testing.T) {
	type call struct{ name, text string }
	rows := []struct {
		row   string
		class string
		calls []call
	}{
		{"shell", ToolShell, []call{{"Bash", "make check"}, {"run_command", "go test ./..."}, {"Bash", "echo bottown"}, {"bash", "make check"}}},
		{"read", ToolRead, []call{{"Read", "AGENTS.md"}, {"read_file", ""}, {"view_file", ""}, {"list_dir", ""}, {"view", ""}, {"ls", ""}}},
		{"edit", ToolEdit, []call{{"Edit", ""}, {"Write", ""}, {"NotebookEdit", ""}, {"edit_file", ""}, {"write_file", ""},
			{"replace_file_content", ""}, {"multi_replace_file_content", ""}, {"edit", ""}, {"multiedit", ""}, {"write", ""}}},
		{"search", ToolSearch, []call{{"Grep", ""}, {"Glob", ""}, {"grep_search", ""}, {"find_by_name", ""}, {"grep", ""}, {"glob", ""}}},
		{"forge mcp", ToolForge, []call{{"mcp__bottown__issue_get", ""}, {"mcp__bottown__review_submit", ""}, {"mcp_bottown_pr_get", ""},
			{"whoami", ""}, {"forge_status", ""}, {"forge_api", ""}, {"issue_get", ""}, {"issue_create", ""}, {"issue_list", ""},
			{"pr_get", ""}, {"pr_list", ""}, {"pr_create", ""}, {"comment", ""}, {"review_submit", ""}}},
		{"forge shell", ToolForge, []call{{"Bash", "bottown issue show 12"}, {"Bash", "/Users/me/.local/bin/bottown pr create --title x"},
			{"Bash", "~/.local/bin/bottown whoami"}, {"run_command", "bottown run end --summary-file -"}, {"bash", "bottown whoami"}}},
		{"browser", ToolBrowser, []call{{"browser_navigate", ""}, {"mcp__chrome-devtools__take_screenshot", ""},
			{"navigate_page", ""}, {"take_snapshot", ""}, {"evaluate_script", ""}}},
		{"other", ToolOther, []call{{"Task", ""}, {"WebFetch", ""}, {"mcp__other__bash", ""}, {"mcp_other_bash", ""}, {"BASH", "bottown whoami"}}},
	}
	for _, r := range rows {
		t.Run(r.row, func(t *testing.T) {
			for _, c := range r.calls {
				if got := toolClass(c.name, c.text); got != r.class {
					t.Errorf("toolClass(%q, %q) = %q, want %q", c.name, c.text, got, r.class)
				}
			}
		})
	}
}
