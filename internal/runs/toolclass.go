package runs

import "strings"

// Tool classes color tool lines in the live tail by what the tool does.
const (
	ToolShell   = "shell"
	ToolRead    = "read"
	ToolEdit    = "edit"
	ToolSearch  = "search"
	ToolForge   = "forge"
	ToolBrowser = "browser"
	ToolOther   = "other"
)

// toolClasses maps the tool names the harnesses use to a class: exact
// names, or prefixes for families of MCP tools. The first matching row
// wins. A shell command that runs bottown is forge (see toolClass).
var toolClasses = []struct {
	class    string
	names    []string
	prefixes []string
}{
	// claude and kimi; codex commands are rendered as Bash. agy.
	{class: ToolShell, names: []string{"Bash", "run_command"}},
	{class: ToolRead, names: []string{"Read", "read_file", "view_file", "list_dir"}},
	{class: ToolEdit, names: []string{"Edit", "Write", "NotebookEdit", "edit_file", "write_file", "replace_file_content", "multi_replace_file_content"}},
	{class: ToolSearch, names: []string{"Grep", "Glob", "grep_search", "find_by_name"}},
	// bottown's MCP tools, by their prefixed and bare names
	// (internal/server/mcp.go and tools.go).
	{class: ToolForge, prefixes: []string{"mcp__bottown__"}, names: []string{
		"whoami", "forge_status", "forge_api", "issue_get", "issue_create", "issue_list",
		"pr_get", "pr_list", "pr_create", "comment", "review_submit",
	}},
	// agy's browser_* tools, and chrome-devtools MCP.
	{class: ToolBrowser, prefixes: []string{"browser_", "mcp__chrome-devtools__", "mcp__chrome_devtools__"}, names: []string{
		"navigate_page", "new_page", "close_page", "list_pages", "select_page",
		"take_screenshot", "take_snapshot", "evaluate_script",
		"list_console_messages", "list_network_requests",
	}},
}

// toolClass classifies a tool call by the tool's name and, for shell
// commands, its argument text: a command whose first word is bottown, or
// ends in /bottown, is forge.
func toolClass(name, text string) string {
	for _, row := range toolClasses {
		match := false
		for _, n := range row.names {
			if name == n {
				match = true
				break
			}
		}
		for _, p := range row.prefixes {
			if strings.HasPrefix(name, p) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if row.class == ToolShell && isBottownCommand(text) {
			return ToolForge
		}
		return row.class
	}
	return ToolOther
}

func isBottownCommand(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return false
	}
	w := strings.Trim(f[0], `"'`)
	return w == "bottown" || strings.HasSuffix(w, "/bottown")
}
