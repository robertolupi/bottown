// Package hostctl drives the host side of an instance: the Docker CLI (so
// docker contexts and Docker Desktop work unchanged) and the host secret
// store.
package hostctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Docker runs the docker CLI.
type Docker struct {
	Bin    string // defaults to "docker"
	Stderr io.Writer
}

func (d Docker) cmd(ctx context.Context, args ...string) *exec.Cmd {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	return exec.CommandContext(ctx, bin, args...)
}

func (d Docker) output(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	c := d.cmd(ctx, args...)
	c.Stdin = stdin
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Ping checks the daemon is reachable.
func (d Docker) Ping(ctx context.Context) error {
	if _, err := d.output(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker is not reachable (is Docker Desktop running?): %w", err)
	}
	return nil
}

// ImageID returns the ID of a local image, or "" if absent.
func (d Docker) ImageID(ctx context.Context, ref string) (string, error) {
	out, err := d.output(ctx, nil, "image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil {
		if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "No such object") {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// Build runs `docker build`. contextDir may be "-" with tar supplied on stdin.
func (d Docker) Build(ctx context.Context, contextDir string, stdin io.Reader, dockerfile, tag string, buildArgs map[string]string) error {
	// No provenance attestation: it embeds a timestamp, so every build got a
	// new image ID and `up` recreated the container even when nothing changed.
	args := []string{"build", "--provenance=false", "-f", dockerfile, "-t", tag}
	for k, v := range buildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}
	args = append(args, contextDir)
	c := d.cmd(ctx, args...)
	c.Stdin = stdin
	c.Stdout, c.Stderr = d.stderr(), d.stderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}

func (d Docker) stderr() io.Writer {
	if d.Stderr != nil {
		return d.Stderr
	}
	return os.Stderr
}

// Container is the state of a container.
type Container struct {
	Exists  bool
	Running bool
	ImageID string
	Labels  map[string]string
}

// Inspect returns the state of container name.
func (d Docker) Inspect(ctx context.Context, name string) (Container, error) {
	out, err := d.output(ctx, nil, "container", "inspect", name)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "No such object") {
			return Container{}, nil
		}
		return Container{}, err
	}
	var v []struct {
		Image  string
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) == 0 {
		return Container{}, fmt.Errorf("parse docker inspect: %v", err)
	}
	return Container{Exists: true, Running: v[0].State.Running, ImageID: v[0].Image, Labels: v[0].Config.Labels}, nil
}

// RunSpec describes a detached container.
type RunSpec struct {
	Name   string
	Image  string
	Ports  []string // host:container mappings, e.g. 127.0.0.1:3030:3000
	Volume string   // name:/path
	Env    map[string]string
	Labels map[string]string
}

// Run starts a new detached container with restart policy unless-stopped.
func (d Docker) Run(ctx context.Context, s RunSpec) error {
	args := []string{"run", "-d", "--name", s.Name, "--hostname", s.Name, "--restart", "unless-stopped"}
	for _, p := range s.Ports {
		args = append(args, "-p", p)
	}
	if s.Volume != "" {
		args = append(args, "-v", s.Volume)
	}
	for k, v := range s.Env {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range s.Labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, s.Image)
	_, err := d.output(ctx, nil, args...)
	return err
}

// Start starts a stopped container.
func (d Docker) Start(ctx context.Context, name string) error {
	_, err := d.output(ctx, nil, "start", name)
	return err
}

// Remove force-removes a container.
func (d Docker) Remove(ctx context.Context, name string) error {
	_, err := d.output(ctx, nil, "rm", "-f", name)
	return err
}

// Stop stops a container.
func (d Docker) Stop(ctx context.Context, name string) error {
	_, err := d.output(ctx, nil, "stop", name)
	return err
}

// RemoveVolume removes a named volume; a missing volume is not an error.
func (d Docker) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.output(ctx, nil, "volume", "rm", name)
	if err != nil && strings.Contains(err.Error(), "no such volume") {
		return nil
	}
	return err
}

// RemoveImage removes an image tag.
func (d Docker) RemoveImage(ctx context.Context, ref string) error {
	_, err := d.output(ctx, nil, "image", "rm", ref)
	return err
}

// Exec runs a command in a container as user and returns its stdout.
func (d Docker) Exec(ctx context.Context, name, user string, args ...string) (string, error) {
	return d.output(ctx, nil, append([]string{"exec", "-u", user, name}, args...)...)
}

// Logs copies container logs to w.
func (d Docker) Logs(ctx context.Context, name string, follow bool, tail string, w io.Writer) error {
	args := []string{"logs", "--tail", tail}
	if follow {
		args = append(args, "-f")
	}
	c := d.cmd(ctx, append(args, name)...)
	c.Stdout, c.Stderr = w, w
	return c.Run()
}
