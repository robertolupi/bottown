package webui

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed suggested_commands.json
var suggestedCommandJSON []byte

var commandTemplates = func() map[string]string {
	var templates map[string]string
	if err := json.Unmarshal(suggestedCommandJSON, &templates); err != nil {
		panic(err)
	}
	return templates
}()

// suggestedCommand fills one of the templates shared with the browser.
func suggestedCommand(name string, values ...string) string {
	return strings.NewReplacer(values...).Replace(commandTemplates[name])
}

// SuggestedCommands exports every built-in command the UI can suggest, with
// concrete sample values replacing all placeholders. Repository-declared
// after-pull steps are arbitrary owner commands and are not part of this set.
func SuggestedCommands() []string {
	names := make([]string, 0, len(commandTemplates))
	for name := range commandTemplates {
		names = append(names, name)
	}
	sort.Strings(names)
	commands := make([]string, 0, len(names))
	for _, name := range names {
		commands = append(commands, suggestedCommand(name,
			"NUMBER", "42", "IMPLEMENT", "claude", "VERIFY", "codex",
			"SLUG", "demo", "POOL", "claude=*,codex=1", "PARALLEL", "2",
			"BRANCH", "main", "ID", "r-1234abcd"))
	}
	return commands
}
