package cli

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/robertolupi/bottown/internal/webui"
	"github.com/spf13/cobra"
)

//go:embed harness/*.md
var harnessFS embed.FS

var harnessTmpl = template.Must(template.ParseFS(harnessFS, "harness/*.md"))

// harnessMarker identifies files bottown wrote, so setup can refresh them
// and never overwrites a file someone else wrote.
const harnessMarker = "written by `bottown harness setup`; re-run it to refresh"

// harnessCommands describes, per harness, where its global slash commands
// live and which templates render them: one for implement and verify, and
// one for discuss, which also takes a free-text question. skillDirs puts
// each command in its own directory as SKILL.md.
var harnessCommands = map[string]struct {
	dir               func(home string) string
	template, discuss string
	skillDirs         bool
	invoke            string
}{
	// Claude Code user commands: /bottown-implement 12 runs `bottown run
	// start` itself (the !`...` line) and inlines the goal prompt.
	"claude": {func(h string) string { return filepath.Join(h, ".claude", "commands") }, "claude-command.md", "claude-discuss.md", false, "/"},
	// Antigravity global workflows: invoked as /bottown-implement; the agent
	// runs `bottown run start` with the number the user typed.
	"agy": {func(h string) string { return filepath.Join(h, ".gemini", "config", "global_workflows") }, "agy-workflow.md", "agy-discuss.md", false, "/"},
	// Kimi Code user skills: invoked as /skill:bottown-implement; the agent
	// runs `bottown run start` like agy's.
	"kimi": {kimiSkillsDir, "kimi-skill.md", "kimi-discuss.md", true, "/skill:"},
	// Codex user skills: ~/.codex/skills/bottown-{implement,verify,discuss}/SKILL.md,
	// invoked as $bottown-implement.
	"codex": {codexSkillsDir, "codex-skill.md", "codex-discuss.md", true, "$"},
}

// kimiSkillsDir is $KIMI_CODE_HOME/skills, default ~/.kimi-code/skills.
func kimiSkillsDir(home string) string {
	if h := os.Getenv("KIMI_CODE_HOME"); h != "" {
		return filepath.Join(h, "skills")
	}
	return filepath.Join(home, ".kimi-code", "skills")
}

// codexSkillsDir is $CODEX_HOME/skills, default ~/.codex/skills.
func codexSkillsDir(home string) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "skills")
	}
	return filepath.Join(home, ".codex", "skills")
}

type commandData struct {
	Kind, Target, Bottown, Marker string
}

func newHarness() *cobra.Command {
	h := &cobra.Command{Use: "harness", Short: "Set up agent harnesses for bottown"}
	var force bool
	setup := &cobra.Command{
		Use:   "setup [claude|agy|kimi|codex]...",
		Short: "Install /bottown-implement, /bottown-verify and /bottown-discuss in harnesses' global commands",
		Long: `Install /bottown-implement, /bottown-verify and /bottown-discuss for each
harness (default: all):

  claude  ~/.claude/commands/bottown-{implement,verify,discuss}.md
  agy     ~/.gemini/config/global_workflows/bottown-{implement,verify,discuss}.md
  kimi    ~/.kimi-code/skills/bottown-{implement,verify,discuss}/SKILL.md
          (invoked as /skill:bottown-implement 12)
  codex   ~/.codex/skills/bottown-{implement,verify,discuss}/SKILL.md
          (invoked as $bottown-implement 12)

Inside an actor worktree, /bottown-implement 12 starts a run for that worktree's
actor (bottown run start implement 12) and works the issue. /bottown-discuss 12
QUESTION starts a discuss run: the agent answers on the issue, without
implementing it. The files name this
bottown binary by absolute path. Re-running refreshes them; a same-named file
bottown did not write is left alone unless --force.

Codex setup also installs allow rules for this binary and plain bottown in
$CODEX_HOME/rules/default.rules (default ~/.codex/rules/default.rules), preserving
other rules. Restart Codex to load them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"claude", "agy", "kimi", "codex"}
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			for _, name := range args {
				if err := installCommands(cmd, home, name, force); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			return nil
		},
	}
	setup.Flags().BoolVar(&force, "force", false, "overwrite same-named commands that bottown did not write")
	h.AddCommand(setup)
	return h
}

func installCommands(cmd *cobra.Command, home, harness string, force bool) error {
	hc, ok := harnessCommands[harness]
	if !ok {
		names := []string{}
		for n := range harnessCommands {
			names = append(names, n)
		}
		slices.Sort(names)
		return fmt.Errorf("unknown harness (known: %s)", strings.Join(names, ", "))
	}
	dir := hc.dir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, c := range []commandData{
		{Kind: "implement", Target: "issue"},
		{Kind: "verify", Target: "pull request"},
		{Kind: "discuss", Target: "issue"},
	} {
		c.Bottown, c.Marker = selfPath(), harnessMarker
		tmpl := hc.template
		if c.Kind == "discuss" {
			tmpl = hc.discuss
		}
		var b bytes.Buffer
		if err := harnessTmpl.ExecuteTemplate(&b, tmpl, c); err != nil {
			return err
		}
		path := filepath.Join(dir, "bottown-"+c.Kind+".md")
		if hc.skillDirs {
			path = filepath.Join(dir, "bottown-"+c.Kind, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
		}
		old, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		case bytes.Equal(old, b.Bytes()):
			fmt.Fprintf(cmd.OutOrStdout(), "%s: up to date\n", path)
			continue
		case !bytes.Contains(old, []byte(harnessMarker)) && !force:
			return fmt.Errorf("%s exists and was not written by bottown; use --force to replace it", path)
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			return err
		}
		invoke := "/"
		if hc.invoke != "" {
			invoke = hc.invoke
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: installed %sbottown-%s\n", path, invoke, c.Kind)
	}
	if harness == "codex" {
		return installCodexRules(cmd, home)
	}
	return nil
}

// checkHarnessCurrent checks whether all commands for known harnesses are installed and up to date.
func checkHarnessCurrent(home string) []webui.HarnessCommandInfo {
	if home == "" {
		return nil
	}
	var res []webui.HarnessCommandInfo
	for _, harness := range []string{"claude", "agy", "kimi", "codex"} {
		hc, ok := harnessCommands[harness]
		if !ok {
			continue
		}
		info := webui.HarnessCommandInfo{Harness: harness}
		dir := hc.dir(home)
		info.Path = dir

		allInstalled := true
		allCurrent := true

		for _, c := range []commandData{
			{Kind: "implement", Target: "issue"},
			{Kind: "verify", Target: "pull request"},
			{Kind: "discuss", Target: "issue"},
		} {
			c.Bottown, c.Marker = selfPath(), harnessMarker
			tmpl := hc.template
			if c.Kind == "discuss" {
				tmpl = hc.discuss
			}
			var b bytes.Buffer
			if err := harnessTmpl.ExecuteTemplate(&b, tmpl, c); err != nil {
				allCurrent = false
				continue
			}
			path := filepath.Join(dir, "bottown-"+c.Kind+".md")
			if hc.skillDirs {
				path = filepath.Join(dir, "bottown-"+c.Kind, "SKILL.md")
			}
			old, err := os.ReadFile(path)
			if err != nil {
				allInstalled = false
				allCurrent = false
				break
			}
			if !bytes.Equal(old, b.Bytes()) {
				allCurrent = false
			}
		}
		info.Installed = allInstalled
		info.Current = allInstalled && allCurrent
		res = append(res, info)
	}
	return res
}

// Keep our rules in one section so a new binary path replaces the old one
// without changing user rules, including more restrictive rules.
const codexRulesBegin = "# BEGIN bottown harness setup rules"
const codexRulesEnd = "# END bottown harness setup rules"

func installCodexRules(cmd *cobra.Command, home string) error {
	path := filepath.Join(filepath.Dir(codexSkillsDir(home)), "rules", "default.rules")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	block := fmt.Sprintf("%s\nprefix_rule(pattern = [%q], decision = \"allow\")\nprefix_rule(pattern = [\"bottown\"], decision = \"allow\")\n%s\n", codexRulesBegin, selfPath(), codexRulesEnd)
	content := string(old)
	begin, end := strings.Index(content, codexRulesBegin), strings.Index(content, codexRulesEnd)
	if begin >= 0 || end >= 0 {
		if begin < 0 || end < begin || strings.Count(content, codexRulesBegin) != 1 || strings.Count(content, codexRulesEnd) != 1 {
			return fmt.Errorf("%s: malformed bottown rules section; repair its markers before re-running setup", path)
		}
		end += len(codexRulesEnd)
		if end < len(content) && content[end] == '\n' {
			end++
		}
		content = content[:begin] + block + content[end:]
	} else {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += block
	}
	if content == string(old) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: up to date\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: installed bottown allow rules; restart Codex to load them\n", path)
	return nil
}
