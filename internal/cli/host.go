package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/hostctl"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/spf13/cobra"
)

const modulePath = "github.com/robertolupi/bottown"

func (g *globals) docker() hostctl.Docker { return hostctl.Docker{} }

func (g *globals) secrets() (hostctl.Secrets, error) {
	dir, err := config.InstanceDir(g.instance)
	if err != nil {
		return nil, err
	}
	return hostctl.OpenSecrets(g.instance, dir), nil
}

// loadInstance returns the instance config, which must exist.
func (g *globals) loadInstance() (config.Instance, error) {
	inst, ok, err := config.LoadInstance(g.instance)
	if err != nil {
		return inst, err
	}
	if !ok {
		return inst, fmt.Errorf("instance %q is not set up; run `bottown up -i %s`", g.instance, g.instance)
	}
	return inst, nil
}

func newUp(g *globals) *cobra.Command {
	var image, ref, src string
	var ifChanged bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create or converge an instance: image, container, provisioning, owner hand-off",
		Long: `Bring an instance up. Re-running converges and is otherwise a no-op.

prod only runs an image chosen explicitly (--image, or --ref to build one from a
commit), never a build of a working tree. Other instances default to a build of
the current working tree.

With --ref and --if-changed, up keeps the instance's current image when it was
built from a commit that differs from --ref only in what never reaches the
image (docs/ and Markdown outside the embedded templates), instead of
building and switching to a new one. make install uses it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ifChanged && ref == "" {
				return errors.New("--if-changed needs --ref")
			}
			return up(cmd.Context(), g, cmd.OutOrStdout(), image, ref, src, ifChanged, timeout)
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "run this local image tag")
	cmd.Flags().StringVar(&ref, "ref", "", "build the image from this git commit and run it")
	cmd.Flags().BoolVar(&ifChanged, "if-changed", false, "with --ref: keep the current image if the commits differ only in docs")
	cmd.Flags().StringVar(&src, "src", "", "bottown source checkout (default: the git checkout containing the current directory)")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "how long to wait for the instance to become healthy")
	return cmd
}

func up(ctx context.Context, g *globals, out io.Writer, image, ref, src string, ifChanged bool, timeout time.Duration) error {
	d := g.docker()
	if err := d.Ping(ctx); err != nil {
		return err
	}
	inst, ok, err := config.LoadInstance(g.instance)
	if err != nil {
		return err
	}
	if !ok {
		if inst, err = newInstance(g.instance); err != nil {
			return err
		}
	}

	switch {
	case image != "" && ref != "":
		return errors.New("--image and --ref are mutually exclusive")
	case image != "":
	case ref != "" && ifChanged && sameImageSource(src, inst.Image, ref) && hasImage(ctx, d, inst.Image):
		image = inst.Image
		fmt.Fprintf(out, "keeping image %s: nothing that goes into it changed up to %s\n", image, ref)
	case ref != "":
		if image, err = buildImage(ctx, d, src, ref, g.instance); err != nil {
			return err
		}
	case g.instance == config.DefaultInstance:
		if inst.Image == "" {
			return errors.New("prod runs only an explicitly chosen image: use `bottown up --ref <commit>` or `--image <tag>`")
		}
		image = inst.Image
	default:
		if image, err = buildImage(ctx, d, src, "", g.instance); err != nil {
			return err
		}
	}
	imageID, err := d.ImageID(ctx, image)
	if err != nil {
		return err
	}
	if imageID == "" {
		return fmt.Errorf("image %s not found locally", image)
	}
	inst.Image = image
	if err := config.SaveInstance(inst); err != nil {
		return err
	}

	c, err := d.Inspect(ctx, inst.Container())
	if err != nil {
		return err
	}
	started := true // whether this up (re)started the container
	switch {
	case c.Exists && (c.ImageID != imageID || c.Labels[specLabel] != containerSpec(inst).Labels[specLabel]):
		fmt.Fprintf(out, "recreating %s on image %s (volume %s kept)\n", inst.Container(), image, inst.Volume())
		if err := d.Remove(ctx, inst.Container()); err != nil {
			return err
		}
		if err := runContainer(ctx, d, inst); err != nil {
			return err
		}
	case c.Exists && !c.Running:
		if err := d.Start(ctx, inst.Container()); err != nil {
			return err
		}
	case !c.Exists:
		if err := runContainer(ctx, d, inst); err != nil {
			return err
		}
	default:
		started = false
	}

	h, err := waitHealthy(ctx, inst, timeout)
	if err != nil {
		_ = d.Logs(ctx, inst.Container(), false, "40", os.Stderr)
		return err
	}
	pwStored, err := handOff(ctx, g, d, inst)
	if err != nil {
		return err
	}
	if !started {
		// The boot-time reconcile is old news; converge now and report that.
		if h.Changes, err = reconcileNow(ctx, g, inst); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "bottown %s is up (bottown %s, Forgejo %s)\n", inst.Name, h.Version, h.ForgejoVersion)
	fmt.Fprintf(out, "  Forgejo  %s  login %s\n", inst.ForgejoURL(), inst.OwnerLogin)
	fmt.Fprintf(out, "  bottown  %s  (MCP at /mcp)\n", inst.ServerURL())
	if pwStored {
		fmt.Fprintf(out, "  owner password stored; open Forgejo signed in with: bottown -i %s web forgejo\n", inst.Name)
	}
	if len(h.Changes) == 0 {
		fmt.Fprintln(out, "  no provisioning changes")
	} else {
		fmt.Fprintf(out, "  %d provisioning changes:\n", len(h.Changes))
		for _, c := range h.Changes {
			fmt.Fprintf(out, "    - %s\n", c)
		}
	}
	return nil
}

var loginRE = regexp.MustCompile(`[^a-z0-9_.-]`)

func newInstance(name string) (config.Instance, error) {
	fp, sp, err := config.AllocatePorts(name, portFree)
	if err != nil {
		return config.Instance{}, err
	}
	login := os.Getenv("BOTTOWN_OWNER_LOGIN")
	if login == "" {
		u, err := user.Current()
		if err != nil {
			return config.Instance{}, err
		}
		login = loginRE.ReplaceAllString(strings.ToLower(u.Username), "")
	}
	return config.Instance{
		Name: name, ForgejoPort: fp, ServerPort: sp,
		OwnerLogin: login, OwnerEmail: ownerEmail(os.Getenv("BOTTOWN_OWNER_EMAIL"), gitConfig("user.email")),
	}, nil
}

// ownerEmail picks the owner's email: BOTTOWN_OWNER_EMAIL when set, else
// git's user.email, unless that is an actor identity (an actor worktree sets
// <actor>@bottown.local), which would collide with the actor's account. Empty
// lets the reconciler default to <login>@bottown.local.
func ownerEmail(env, git string) string {
	if env != "" {
		return env
	}
	if strings.HasSuffix(strings.ToLower(git), "@bottown.local") {
		return ""
	}
	return git
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// specLabel records a hash of the run spec, so `up` recreates the container
// when the spec changes (new env, ports), not only when the image does.
const specLabel = "io.bottown.spec"

func containerSpec(inst config.Instance) hostctl.RunSpec {
	spec := hostctl.RunSpec{
		Name:  inst.Container(),
		Image: inst.Image,
		Ports: []string{
			fmt.Sprintf("127.0.0.1:%d:3000", inst.ForgejoPort),
			fmt.Sprintf("127.0.0.1:%d:8080", inst.ServerPort),
		},
		Volume: inst.Volume() + ":/data",
		Env: map[string]string{
			"FORGEJO__server__ROOT_URL":               inst.ForgejoURL() + "/",
			"FORGEJO__server__DOMAIN":                 "127.0.0.1",
			"FORGEJO__session__COOKIE_NAME":           config.ForgejoCookie(inst.Name),
			"FORGEJO__security__COOKIE_REMEMBER_NAME": config.ForgejoCookie(inst.Name) + "_remember",
			"BOTTOWN_INSTANCE":                        inst.Name,
			"BOTTOWN_OWNER_LOGIN":                     inst.OwnerLogin,
			"BOTTOWN_OWNER_EMAIL":                     inst.OwnerEmail,
			"BOTTOWN_OWNER_FULLNAME":                  gitConfig("user.name"),
		},
		Labels: map[string]string{"io.bottown.instance": inst.Name},
	}
	b, _ := json.Marshal(struct {
		Ports  []string
		Volume string
		Env    map[string]string
	}{spec.Ports, spec.Volume, spec.Env})
	sum := sha256.Sum256(b)
	spec.Labels[specLabel] = hex.EncodeToString(sum[:8])
	return spec
}

func runContainer(ctx context.Context, d hostctl.Docker, inst config.Instance) error {
	return d.Run(ctx, containerSpec(inst))
}

func fetchHealth(ctx context.Context, inst config.Instance) (server.Health, error) {
	var h server.Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.ServerURL()+"/healthz", nil)
	if err != nil {
		return h, err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("healthz: %s", resp.Status)
	}
	return h, json.NewDecoder(resp.Body).Decode(&h)
}

func waitHealthy(ctx context.Context, inst config.Instance, timeout time.Duration) (server.Health, error) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		h, err := fetchHealth(ctx, inst)
		switch {
		case err == nil && h.Reconciled:
			return h, nil
		case err == nil:
			last = "forgejo " + h.Forgejo
			if h.Error != "" {
				last += ": " + h.Error
			}
		default:
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return h, fmt.Errorf("instance %s not healthy after %s: %s", inst.Name, timeout, last)
		}
		select {
		case <-ctx.Done():
			return h, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// handOff makes sure the host holds a working owner API key, fetching one
// (and any pending initial password) through docker exec when it does not.
// It reports whether a new password was stored.
func handOff(ctx context.Context, g *globals, d hostctl.Docker, inst config.Instance) (bool, error) {
	sec, err := g.secrets()
	if err != nil {
		return false, err
	}
	if key, ok, err := sec.Get(hostctl.AccountAPIKey); err != nil {
		return false, err
	} else if ok {
		if actor, err := whoami(ctx, inst, key); err == nil && actor == inst.OwnerLogin {
			return false, nil
		}
	}
	raw, err := d.Exec(ctx, inst.Container(), "git", "bottown", "admin", "owner-credentials")
	if err != nil {
		return false, err
	}
	var oc OwnerCredentials
	if err := json.Unmarshal([]byte(raw), &oc); err != nil {
		return false, fmt.Errorf("parse owner credentials: %w", err)
	}
	if err := sec.Set(hostctl.AccountAPIKey, oc.APIKey); err != nil {
		return false, err
	}
	if oc.Password == "" {
		return false, nil
	}
	if have, ok, _ := sec.Get(hostctl.AccountPassword); ok && have == oc.Password {
		return false, nil
	}
	return true, sec.Set(hostctl.AccountPassword, oc.Password)
}

func reconcileNow(ctx context.Context, g *globals, inst config.Instance) ([]string, error) {
	key, err := ownerKey(g)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.ServerURL()+"/v1/reconcile", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Changes []string `json:"changes"`
		Error   string   `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("reconcile: %s", resp.Status)
	}
	if v.Error != "" {
		return v.Changes, fmt.Errorf("reconcile: %s", v.Error)
	}
	return v.Changes, nil
}

func whoami(ctx context.Context, inst config.Instance, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.ServerURL()+"/v1/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whoami: %s", resp.Status)
	}
	var v struct{ Actor string }
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v.Actor, err
}

// findSource locates the bottown source checkout.
func findSource(src string) (string, error) {
	if src == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", errors.New("not inside a git checkout; pass --src with the bottown source checkout")
		}
		src = strings.TrimSpace(string(out))
	}
	b, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil || !strings.Contains(string(b), "module "+modulePath+"\n") {
		return "", fmt.Errorf("%s is not a bottown source checkout; pass --src", src)
	}
	return src, nil
}

// imageInputs are the pathspecs, relative to the source checkout, of what
// goes into a bottown image: everything but the docs. Markdown under
// internal/ is embedded in the binary, so only docs/ and the top-level and
// docker/ READMEs are left out.
var imageInputs = []string{".", ":(exclude)docs", ":(exclude,glob)*.md", ":(exclude)docker/README.md"}

// sameImageSource reports whether image is a commit image (bottown:<commit>,
// as buildImage tags them) built from a commit whose image inputs equal ref's.
func sameImageSource(src, image, ref string) bool {
	built, ok := strings.CutPrefix(image, "bottown:")
	if !ok || built == "" {
		return false
	}
	src, err := findSource(src)
	if err != nil {
		return false
	}
	old := strings.TrimSpace(gitOut(src, "rev-parse", "--verify", "--quiet", built+"^{commit}"))
	sha := strings.TrimSpace(gitOut(src, "rev-parse", "--verify", "--quiet", ref+"^{commit}"))
	if old == "" || sha == "" || !strings.HasPrefix(old, built) {
		return false
	}
	args := append([]string{"-C", src, "diff", "--quiet", old, sha, "--"}, imageInputs...)
	return exec.Command("git", args...).Run() == nil
}

func hasImage(ctx context.Context, d hostctl.Docker, image string) bool {
	id, err := d.ImageID(ctx, image)
	return err == nil && id != ""
}

// buildImage builds bottown:<commit> from ref, or bottown:<instance>-wt from
// the working tree when ref is empty, and returns the tag.
func buildImage(ctx context.Context, d hostctl.Docker, src, ref, instance string) (string, error) {
	src, err := findSource(src)
	if err != nil {
		return "", err
	}
	if ref == "" {
		version := strings.TrimSpace(gitOut(src, "describe", "--always", "--dirty", "--abbrev=12"))
		if version == "" {
			version = "uncommitted"
		}
		tag := "bottown:" + instance + "-wt"
		err := d.Build(ctx, src, nil, "docker/Dockerfile", tag, map[string]string{"VERSION": version})
		return tag, err
	}
	sha := strings.TrimSpace(gitOut(src, "rev-parse", "--verify", ref+"^{commit}"))
	if sha == "" {
		return "", fmt.Errorf("unknown git ref %q in %s", ref, src)
	}
	short := sha[:12]
	tag := "bottown:" + short
	if id, err := d.ImageID(ctx, tag); err != nil || id != "" {
		return tag, err
	}
	archive := exec.CommandContext(ctx, "git", "-C", src, "archive", "--format=tar", sha)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return "", err
	}
	archive.Stderr = os.Stderr
	if err := archive.Start(); err != nil {
		return "", err
	}
	berr := d.Build(ctx, "-", pipe, "docker/Dockerfile", tag, map[string]string{"VERSION": short})
	if werr := archive.Wait(); berr == nil && werr != nil {
		berr = fmt.Errorf("git archive: %w", werr)
	}
	return tag, berr
}

func gitOut(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out)
}

func newImage(g *globals) *cobra.Command {
	img := &cobra.Command{Use: "image", Short: "Build bottown images"}
	var ref, src string
	build := &cobra.Command{
		Use:   "build",
		Short: "Build bottown:<commit> from --ref, or bottown:<instance>-wt from the working tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tag, err := buildImage(cmd.Context(), g.docker(), src, ref, g.instance)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), tag)
			return nil
		},
	}
	build.Flags().StringVar(&ref, "ref", "", "git commit to build (default: the working tree)")
	build.Flags().StringVar(&src, "src", "", "bottown source checkout")
	img.AddCommand(build)
	return img
}

func newDown(g *globals) *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop an instance; --purge also deletes its container, volume and host config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, d := cmd.Context(), g.docker()
			inst, _, err := config.LoadInstance(g.instance)
			if err != nil {
				return err
			}
			c, err := d.Inspect(ctx, inst.Container())
			if err != nil {
				return err
			}
			if !purge {
				if c.Running {
					return d.Stop(ctx, inst.Container())
				}
				return nil
			}
			if c.Exists {
				if err := d.Remove(ctx, inst.Container()); err != nil {
					return err
				}
			}
			if err := d.RemoveVolume(ctx, inst.Volume()); err != nil {
				return err
			}
			// The working-tree image is per instance; commit images are shared.
			if id, _ := d.ImageID(ctx, "bottown:"+inst.Name+"-wt"); id != "" {
				_ = d.RemoveImage(ctx, "bottown:"+inst.Name+"-wt")
			}
			sec, err := g.secrets()
			if err != nil {
				return err
			}
			_ = sec.Delete(hostctl.AccountAPIKey)
			_ = sec.Delete(hostctl.AccountPassword)
			dir, err := config.InstanceDir(g.instance)
			if err != nil {
				return err
			}
			return os.RemoveAll(dir)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "delete all instance data (irreversible)")
	return cmd
}

func printStatus(out io.Writer, r *lipgloss.Renderer, inst config.Instance, source, state string, h *server.Health, healthErr error) {
	bold := r.NewStyle().Bold(true).Render
	label := func(w string) string {
		pad := 10 - len(w)
		if pad < 1 {
			pad = 1
		}
		return bold(w) + strings.Repeat(" ", pad)
	}
	fmt.Fprintf(out, "%s%s (from %s)\n%s%s (%s)\n%s%s\n%s%s\n%s%s\n",
		label("instance"), inst.Name, source,
		label("container"), inst.Container(), state,
		label("image"), inst.Image,
		label("forgejo"), inst.ForgejoURL(),
		label("bottown"), inst.ServerURL())
	if healthErr != nil {
		fmt.Fprintf(out, "%sunreachable: %v\n", label("health"), healthErr)
	} else if h != nil {
		fmt.Fprintf(out, "%sforgejo=%s reconciled=%v version=%s forgejo_version=%s\n",
			label("health"), h.Forgejo, h.Reconciled, h.Version, h.ForgejoVersion)
		if h.Error != "" {
			fmt.Fprintf(out, "%s%s\n", label("error"), h.Error)
		}
	}
}

func newStatus(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the container and provisioning state of an instance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			c, err := g.docker().Inspect(cmd.Context(), inst.Container())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			state := "absent"
			if c.Exists {
				state = "stopped"
				if c.Running {
					state = "running"
				}
			}
			var (
				h         *server.Health
				healthErr error
			)
			if c.Running {
				res, err := fetchHealth(cmd.Context(), inst)
				if err != nil {
					healthErr = err
				} else {
					h = &res
				}
			}
			printStatus(out, lipgloss.NewRenderer(out), inst, g.source, state, h, healthErr)
			return nil
		},
	}
}

func newLogs(g *globals) *cobra.Command {
	var follow bool
	var tail string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show container logs (Forgejo and bottown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			return g.docker().Logs(cmd.Context(), inst.Container(), follow, tail, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow")
	cmd.Flags().StringVar(&tail, "tail", "100", "lines to show")
	return cmd
}
