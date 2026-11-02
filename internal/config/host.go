package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DefaultInstance is the instance used when none is selected.
const DefaultInstance = "prod"

var instanceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// ValidInstance checks an instance name; it ends up in container, volume and
// git remote names.
func ValidInstance(name string) error {
	if !instanceRE.MatchString(name) {
		return fmt.Errorf("invalid instance name %q (want lowercase letters, digits, dashes)", name)
	}
	return nil
}

// Instance is the host-side record of one bottown installation. It holds no
// secrets: those live in the Keychain (or the fallback secret store).
type Instance struct {
	Name        string `toml:"name"`
	ForgejoPort int    `toml:"forgejo_port"`
	ServerPort  int    `toml:"server_port"`
	Image       string `toml:"image,omitempty"`
	OwnerLogin  string `toml:"owner_login,omitempty"`
	OwnerEmail  string `toml:"owner_email,omitempty"`
	// ActorModels is the default model spec of each actor's launched runs
	// on this host, used when the command names none (ACTOR without
	// :SPEC), e.g. agy = "gemini-3.8-flash".
	ActorModels map[string]string `toml:"actor_models,omitempty"`
}

// ActorModel is an actor's default spec on this host, or "".
func (i Instance) ActorModel(actor string) string { return i.ActorModels[actor] }

// Container is the Docker container name.
func (i Instance) Container() string { return "bottown-" + i.Name }

// Volume is the Docker volume name.
func (i Instance) Volume() string { return "bottown-" + i.Name + "-data" }

// Remote is the git remote name used for this instance.
func (i Instance) Remote() string {
	if i.Name == DefaultInstance {
		return "bottown"
	}
	return "bottown-" + i.Name
}

// ForgejoCookie is Forgejo's session cookie name for this instance. Cookies
// are scoped by host, not port, so instances on 127.0.0.1 need distinct names.
func ForgejoCookie(instance string) string { return "bottown_" + cookieSafe(instance) + "_forgejo" }

// UICookie is the bottown web UI session cookie name for this instance.
func UICookie(instance string) string { return "bottown_" + cookieSafe(instance) + "_ui" }

func cookieSafe(s string) string { return strings.ReplaceAll(s, "-", "_") }

// ForgejoURL is the owner-facing Forgejo web URL.
func (i Instance) ForgejoURL() string { return fmt.Sprintf("http://127.0.0.1:%d", i.ForgejoPort) }

// ServerURL is the bottown server URL.
func (i Instance) ServerURL() string { return fmt.Sprintf("http://127.0.0.1:%d", i.ServerPort) }

// Home returns the bottown host directory, honouring $BOTTOWN_HOME.
func Home() (string, error) {
	if h := os.Getenv("BOTTOWN_HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".bottown"), nil
}

// InstanceDir is ~/.bottown/<name>.
func InstanceDir(name string) (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, name), nil
}

// LoadInstance reads an instance's config. ok is false if it does not exist.
func LoadInstance(name string) (inst Instance, ok bool, err error) {
	dir, err := InstanceDir(name)
	if err != nil {
		return inst, false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return Instance{Name: name}, false, nil
	}
	if err != nil {
		return inst, false, err
	}
	if err := toml.Unmarshal(b, &inst); err != nil {
		return inst, false, fmt.Errorf("parse %s config: %w", name, err)
	}
	inst.Name = name
	return inst, true, nil
}

// SaveInstance writes an instance's config with 0600 under a 0700 dir.
func SaveInstance(inst Instance) error {
	dir, err := InstanceDir(inst.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(inst)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.toml"), b, 0o600)
}

// ListInstances returns the names of all configured instances.
func ListInstances() ([]string, error) {
	h, err := Home()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(h)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && ValidInstance(e.Name()) == nil {
			if _, err := os.Stat(filepath.Join(h, e.Name(), "config.toml")); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	return names, nil
}

// wellKnownPorts are the fixed port pairs for the two long-lived instances.
var wellKnownPorts = map[string][2]int{
	"prod": {3030, 3031},
	"dev":  {3040, 3041},
}

// AllocatePorts picks a (forgejo, server) port pair for a new instance: the
// well-known pair for prod and dev, otherwise the first pair from 3050 up
// that no other instance has claimed and that free() accepts.
func AllocatePorts(name string, free func(int) bool) (int, int, error) {
	if p, ok := wellKnownPorts[name]; ok {
		return p[0], p[1], nil
	}
	used := map[int]bool{}
	for _, p := range wellKnownPorts {
		used[p[0]], used[p[1]] = true, true
	}
	names, err := ListInstances()
	if err != nil {
		return 0, 0, err
	}
	for _, n := range names {
		if inst, ok, _ := LoadInstance(n); ok {
			used[inst.ForgejoPort], used[inst.ServerPort] = true, true
		}
	}
	for p := 3050; p < 3990; p += 2 {
		if !used[p] && !used[p+1] && free(p) && free(p+1) {
			return p, p + 1, nil
		}
	}
	return 0, 0, errors.New("no free port pair in 3050-3990")
}
