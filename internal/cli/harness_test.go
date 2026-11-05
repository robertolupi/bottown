package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/runs"
)

func TestInstallCommands(t *testing.T) {
	home := t.TempDir()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CRUSH_SKILLS_DIR", "")
	for _, h := range []string{"claude", "agy", "kimi", "codex", "crush"} {
		if err := installCommands(cmd, home, h, false); err != nil {
			t.Fatal(err)
		}
	}
	impl, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "bottown-implement.md"))
	if err != nil {
		t.Fatal(err)
	}
	self := selfPath()
	for _, want := range []string{self + " run start implement <number>", "\n> $ARGUMENTS\n", "allowed-tools: Bash(" + self + ":*)", harnessMarker, "argument-hint: <issue number> [note]"} {
		if !strings.Contains(string(impl), want) {
			t.Errorf("claude implement command lacks %q:\n%s", want, impl)
		}
	}
	ver, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "global_workflows", "bottown-verify.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ver), self+" run start verify <number>") || !strings.Contains(string(ver), "/bottown-verify") {
		t.Errorf("agy verify workflow:\n%s", ver)
	}
	dis, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "bottown-discuss.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{self + " run start discuss <number>", "> $ARGUMENTS", "argument-hint: <issue number> [question]", harnessMarker} {
		if !strings.Contains(string(dis), want) {
			t.Errorf("claude discuss command lacks %q:\n%s", want, dis)
		}
	}
	adis, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "global_workflows", "bottown-discuss.md"))
	if err != nil || !strings.Contains(string(adis), self+" run start discuss <number>") {
		t.Errorf("agy discuss workflow: %v\n%s", err, adis)
	}
	plan, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "bottown-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{self + " run start plan <number>", "\n> $ARGUMENTS\n", "argument-hint: <kickoff issue number> [note]", harnessMarker} {
		if !strings.Contains(string(plan), want) {
			t.Errorf("claude plan command lacks %q:\n%s", want, plan)
		}
	}
	aplan, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "global_workflows", "bottown-plan.md"))
	if err != nil || !strings.Contains(string(aplan), self+" run start plan <number>") || !strings.Contains(string(aplan), "/bottown-plan") {
		t.Errorf("agy plan workflow: %v\n%s", err, aplan)
	}

	for _, kind := range []string{"implement", "verify", "discuss", "plan"} {
		skill, err := os.ReadFile(filepath.Join(home, ".kimi-code", "skills", "bottown-"+kind, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"name: bottown-" + kind + "\n", "disableModelInvocation: true", "\n> $ARGUMENTS\n", self + " run start " + kind + " <number>", harnessMarker} {
			if !strings.Contains(string(skill), want) {
				t.Errorf("kimi %s skill lacks %q:\n%s", kind, want, skill)
			}
		}
	}

	for _, kind := range []string{"implement", "verify", "discuss", "plan"} {
		skill, err := os.ReadFile(filepath.Join(home, ".codex", "skills", "bottown-"+kind, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"name: bottown-" + kind + "\n", "user's message", self + " run start " + kind + " <number>", harnessMarker} {
			if !strings.Contains(string(skill), want) {
				t.Errorf("codex %s skill lacks %q:\n%s", kind, want, skill)
			}
		}
		if strings.Contains(string(skill), "$ARGUMENTS") {
			t.Errorf("codex %s skill should not use literal $ARGUMENTS:\n%s", kind, skill)
		}
	}

	for _, kind := range []string{"implement", "verify", "discuss", "plan"} {
		skill, err := os.ReadFile(filepath.Join(home, ".config", "crush", "skills", "bottown-"+kind, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"name: bottown-" + kind + "\n", "user-invocable: true", "disable-model-invocation: true", "user's message", self + " run start " + kind + " <number>", harnessMarker} {
			if !strings.Contains(string(skill), want) {
				t.Errorf("crush %s skill lacks %q:\n%s", kind, want, skill)
			}
		}
		if strings.Contains(string(skill), "$ARGUMENTS") {
			t.Errorf("crush %s skill should not use literal $ARGUMENTS:\n%s", kind, skill)
		}
	}

	// The bottown skill, where the harness loads skills on demand, sends the
	// agent to `bottown guide`; agy gets the guide from its workspace rules.
	for h, dir := range map[string]string{
		"claude": filepath.Join(home, ".claude", "skills"),
		"kimi":   filepath.Join(home, ".kimi-code", "skills"),
		"codex":  filepath.Join(home, ".codex", "skills"),
		"crush":  filepath.Join(home, ".config", "crush", "skills"),
	} {
		skill, err := os.ReadFile(filepath.Join(dir, "bottown", "SKILL.md"))
		if err != nil {
			t.Errorf("%s: no bottown skill: %v", h, err)
			continue
		}
		for _, want := range []string{"name: bottown\n", "description: ", self + " guide", harnessMarker} {
			if !strings.Contains(string(skill), want) {
				t.Errorf("%s bottown skill lacks %q:\n%s", h, want, skill)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "config", "global_workflows", "bottown.md")); err == nil {
		t.Error("agy got a bottown skill as a workflow")
	}

	// Re-running is a no-op; a foreign file is protected unless --force.
	out.Reset()
	if err := installCommands(cmd, home, "claude", false); err != nil || strings.Count(out.String(), "up to date") != len(harnessKinds)+1 {
		t.Errorf("second install: %v %s", err, out.String())
	}
	foreign := filepath.Join(home, ".claude", "commands", "bottown-verify.md")
	os.WriteFile(foreign, []byte("my own command\n"), 0o644)
	if err := installCommands(cmd, home, "claude", false); err == nil || !strings.Contains(err.Error(), "not written by bottown") {
		t.Errorf("overwrote a foreign file: %v", err)
	}
	if err := installCommands(cmd, home, "claude", true); err != nil {
		t.Errorf("--force: %v", err)
	}
	if err := installCommands(cmd, home, "nope", false); err == nil {
		t.Error("unknown harness accepted")
	}
}

// The claude commands used to paste $ARGUMENTS into a !`run start` line, so
// a note after the number ("296 investigate why", or on the next line) made
// run start fail, and a quote or # in it would break the shell (#5, #299).
// Every claude command shows the request as text instead, and the agent runs
// `run start` with the number only. (A positional $0 would not do: Claude
// Code splits arguments shell-style, so an apostrophe in the note could break
// it, and "pr #2" would pass "pr".)
func TestClaudeCommandsKeepTheRequestOutOfTheShell(t *testing.T) {
	home := t.TempDir()
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	if err := installCommands(cmd, home, "claude", false); err != nil {
		t.Fatal(err)
	}
	for _, k := range harnessKinds {
		kind := k.Kind
		b, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "bottown-"+kind+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile("(?m)^!`").Match(b) {
			t.Errorf("%s command runs a shell line itself:\n%s", kind, b)
		}
		if strings.Count(string(b), "$ARGUMENTS") != 1 || !strings.Contains(string(b), "\n> $ARGUMENTS\n") {
			t.Errorf("%s: $ARGUMENTS must appear once, as quoted text:\n%s", kind, b)
		}
		if !strings.Contains(string(b), selfPath()+" run start "+kind+" <number>") {
			t.Errorf("%s command does not say how to start the run:\n%s", kind, b)
		}
	}
}

func TestRunTarget(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int64
		err  string
	}{
		{[]string{"12"}, 12, ""},
		{[]string{"#12"}, 12, ""},
		{[]string{"issue", "#12"}, 12, ""},
		{[]string{"issue #12"}, 12, ""},
		{[]string{"pull request !12"}, 12, ""},
		{nil, 0, "give one"},
		{[]string{""}, 0, "give one"},
		{[]string{"12 13"}, 0, "give one"},
		{[]string{"x"}, 0, "not an issue or pull request number"},
	} {
		n, err := runTarget("implement", tc.args)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err %v, want %q", tc.args, err, tc.err)
			}
			continue
		}
		if err != nil || n != tc.want {
			t.Errorf("%q: got %d, %v; want %d", tc.args, n, err, tc.want)
		}
	}
}

func TestDiscussQuestion(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("what's $(this)?\nsecond line\n"))
	if q, err := discussQuestion(cmd, nil, "-"); err != nil || q != "what's $(this)?\nsecond line" {
		t.Errorf("stdin: %q %v", q, err)
	}
	if q, err := discussQuestion(cmd, []string{"blah", "or", "foo?"}, ""); err != nil || q != "blah or foo?" {
		t.Errorf("words: %q %v", q, err)
	}
	if q, err := discussQuestion(cmd, nil, ""); err != nil || q != "" {
		t.Errorf("none: %q %v", q, err)
	}
	if _, err := discussQuestion(cmd, []string{"x"}, "-"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("both: %v", err)
	}
}

func TestDiscussHandover(t *testing.T) {
	rec := runs.Record{ID: "r-1", Kind: "discuss", Actor: "claude", Family: "anthropic", SHA: "abc", Question: "blah\nor foo?"}
	got := handoverText(rec, "succeeded", 0, "answered")
	if !strings.Contains(got, "Discussed against `abc`") || !strings.Contains(got, "Question:\n> blah\n> or foo?\n\n") || !strings.Contains(got, "answered") {
		t.Errorf("handover:\n%s", got)
	}
	rec.Question = ""
	if got := handoverText(rec, "succeeded", 0, ""); strings.Contains(got, "Question:") {
		t.Errorf("handover without a question:\n%s", got)
	}
}

func TestCheckHarnessCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("CODEX_HOME", "")

	// 1. Initially nothing is installed
	statuses := checkHarnessCurrent(home)
	if len(statuses) != 5 {
		t.Fatalf("expected 5 harnesses, got %d", len(statuses))
	}
	for _, s := range statuses {
		if s.Installed || s.Current {
			t.Errorf("harness %s should not be installed in empty home: %+v", s.Harness, s)
		}
	}

	// 2. Install all four harnesses
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	for _, h := range []string{"claude", "agy", "kimi", "codex", "crush"} {
		if err := installCommands(cmd, home, h, false); err != nil {
			t.Fatal(err)
		}
	}

	statuses = checkHarnessCurrent(home)
	for _, s := range statuses {
		if !s.Installed || !s.Current {
			t.Errorf("harness %s should be installed and current: %+v", s.Harness, s)
		}
	}

	// 3. Stale file (claude command modified/stale)
	stalePath := filepath.Join(home, ".claude", "commands", "bottown-verify.md")
	if err := os.WriteFile(stalePath, []byte("stale content with marker "+harnessMarker), 0o644); err != nil {
		t.Fatal(err)
	}

	statuses = checkHarnessCurrent(home)
	for _, s := range statuses {
		if s.Harness == "claude" {
			if !s.Installed {
				t.Errorf("claude should be installed")
			}
			if s.Current {
				t.Errorf("claude should be marked stale/not current when template differs")
			}
		} else {
			if !s.Current {
				t.Errorf("harness %s should still be current: %+v", s.Harness, s)
			}
		}
	}

	// 4. Missing one command file (agy missing discuss)
	missingPath := filepath.Join(home, ".gemini", "config", "global_workflows", "bottown-discuss.md")
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}

	statuses = checkHarnessCurrent(home)
	for _, s := range statuses {
		if s.Harness == "agy" {
			if s.Installed || s.Current {
				t.Errorf("agy should not be installed/current when a command is missing: %+v", s)
			}
		}
	}
}

func TestInstallCodexRules(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprint(custom), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", "")
			root := filepath.Join(home, ".codex")
			if custom {
				root = filepath.Join(t.TempDir(), "custom codex")
				t.Setenv("CODEX_HOME", root)
			}
			path := filepath.Join(root, "rules", "default.rules")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			before := "# user rules\nprefix_rule(pattern = [\"bottown\", \"down\"], decision = \"prompt\")\n"
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := installCommands(cmd, home, "codex", false); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{before, fmt.Sprintf("pattern = [%q]", selfPath()), `pattern = ["bottown"], decision = "allow"`} {
				if !strings.Contains(string(first), want) {
					t.Errorf("rules lack %q: %s", want, first)
				}
			}
			if !strings.Contains(out.String(), "restart Codex") {
				t.Error("missing restart reminder")
			}
			if err := installCommands(cmd, home, "codex", false); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if !bytes.Equal(first, second) {
				t.Error("second setup changed rules")
			}
			// Updating the managed section preserves rules on either side.
			stale := strings.Replace(string(first), selfPath(), "/old/bottown", 1) + "# trailing user rule\n"
			os.WriteFile(path, []byte(stale), 0o600)
			if err := installCodexRules(cmd, home); err != nil {
				t.Fatal(err)
			}
			updated, _ := os.ReadFile(path)
			if strings.Contains(string(updated), "/old/bottown") || !strings.HasSuffix(string(updated), "# trailing user rule\n") {
				t.Fatalf("bad refresh: %s", updated)
			}
			os.WriteFile(path, []byte(before+codexRulesBegin+"\n"), 0o600)
			if err := installCodexRules(cmd, home); err == nil {
				t.Error("accepted broken markers")
			}
			unchanged, _ := os.ReadFile(path)
			if string(unchanged) != before+codexRulesBegin+"\n" {
				t.Error("changed malformed file")
			}
		})
	}
}
