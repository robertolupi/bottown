package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// fakeHarnesses puts agy and crush scripts on PATH that echo their
// arguments, and sets the run testing hook, which listings must ignore.
func fakeHarnesses(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"agy", "crush"} {
		script := "#!/bin/sh\necho " + name + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(runs.HarnessCmdEnv, "echo script")
}

var modelActors = []server.ActorInfo{
	{Name: "claude", Family: "anthropic", Harness: "claude"},
	{Name: "agy", Family: "google", Harness: "antigravity"},
	{Name: "crush", Family: "mixed", Harness: "crush"},
}

func TestModelsOneActor(t *testing.T) {
	fakeHarnesses(t)
	var out, errOut bytes.Buffer
	if err := listModels(context.Background(), &out, &errOut, modelActors, []string{"crush", "openrouter", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "crush models openrouter --json\n" {
		t.Errorf("crush: %q", got)
	}
	out.Reset()
	if err := listModels(context.Background(), &out, &errOut, modelActors, []string{"agy"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "agy models\n" {
		t.Errorf("agy: %q", got)
	}
}

func TestModelsNoListing(t *testing.T) {
	fakeHarnesses(t)
	var out, errOut bytes.Buffer
	if err := listModels(context.Background(), &out, &errOut, modelActors, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "harness claude has no command that lists its models") {
		t.Errorf("claude: %q", got)
	}
	// Arguments for a listing that does not exist are an error.
	if err := listModels(context.Background(), &out, &errOut, modelActors, []string{"claude", "x"}); err == nil {
		t.Error("claude x: no error")
	}
}

func TestModelsUnknownActor(t *testing.T) {
	err := listModels(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, modelActors, []string{"nobody"})
	if err == nil || !strings.Contains(err.Error(), "actors: claude, agy, crush") {
		t.Errorf("err = %v", err)
	}
}

func TestModelsEveryActor(t *testing.T) {
	fakeHarnesses(t)
	var out, errOut bytes.Buffer
	if err := listModels(context.Background(), &out, &errOut, modelActors, nil); err != nil {
		t.Fatal(err)
	}
	want := "== claude (claude)\n" +
		"harness claude has no command that lists its models: give claude:MODEL a name its own model flag takes\n" +
		"\n== agy (antigravity)\nagy models\n" +
		"\n== crush (crush)\ncrush models\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestModelsHarnessFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\necho no >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	err := listModels(context.Background(), &out, &errOut, modelActors, []string{"agy"})
	if err == nil || !strings.Contains(err.Error(), "agy models") {
		t.Errorf("err = %v", err)
	}
	if errOut.String() != "no\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
}
