// Package dockerutil shells out to a container CLI (docker or podman — see
// Client) to manage the disposable PostgreSQL containers restore.sh's Go
// equivalent restores into.
package dockerutil

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// Client runs container commands via the given binary. Docker and Podman
// are both supported — Podman mirrors Docker's `ps`/`inspect`/`run`/`rm`
// flags and Go-template output closely enough that no command here needs
// to differ between them.
type Client struct {
	// Bin is the container CLI binary name (e.g. "docker" or "podman").
	Bin string
}

// New returns a Client that shells out to bin.
func New(bin string) *Client { return &Client{Bin: bin} }

// Container describes one existing "pgcloner-*" container.
type Container struct {
	Name   string
	Status string
}

// ListContainers lists containers whose name starts with "pgcloner-".
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "ps", "-a",
		"--filter", "name=pgcloner-", "--format", "{{.Names}}|{{.Status}}").Output()
	if err != nil {
		return nil, fmt.Errorf("%s ps: %w", c.Bin, err)
	}
	var containers []Container
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		name, status, ok := strings.Cut(line, "|")
		if !ok || name == "" {
			continue
		}
		containers = append(containers, Container{Name: name, Status: status})
	}
	return containers, sc.Err()
}

// Exists reports whether a container with the given name exists (running
// or stopped).
func (c *Client) Exists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "ps", "-a", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, fmt.Errorf("%s ps: %w", c.Bin, err)
	}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// RemoveForce force-removes a container, ignoring "not found" errors.
func (c *Client) RemoveForce(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, c.Bin, "rm", "-f", name).Run()
}

// HostPort inspects a running container's published port for 5432/tcp.
func (c *Client) HostPort(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}`, name).Output()
	if err != nil {
		return "", fmt.Errorf("%s inspect %s: %w", c.Bin, name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// FreePort asks the OS for an ephemeral free TCP port.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// RunPostgres starts a fresh postgres/postgis container publishing 5432 on
// hostPort, removing any pre-existing container with the same name first.
func (c *Client) RunPostgres(ctx context.Context, name, image, password string, hostPort int) error {
	exists, err := c.Exists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		if err := c.RemoveForce(ctx, name); err != nil {
			return fmt.Errorf("remove existing container %s: %w", name, err)
		}
	}
	cmd := exec.CommandContext(ctx, c.Bin, "run", "-d",
		"--name", name,
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD="+password,
		"-p", fmt.Sprintf("%d:5432", hostPort),
		image,
	)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
