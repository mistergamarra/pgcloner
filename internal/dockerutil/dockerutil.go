// Package dockerutil shells out to the docker CLI to manage the disposable
// PostgreSQL containers restore.sh's Go equivalent restores into.
package dockerutil

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Container describes one existing "pgcloner-*" container.
type Container struct {
	Name   string
	Status string
}

// ListContainers lists containers whose name starts with "pgcloner-".
func ListContainers(ctx context.Context) ([]Container, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "name=pgcloner-", "--format", "{{.Names}}|{{.Status}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
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
func Exists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, fmt.Errorf("docker ps: %w", err)
	}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// RemoveForce force-removes a container, ignoring "not found" errors.
func RemoveForce(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

// HostPort inspects a running container's published port for 5432/tcp.
func HostPort(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}`, name).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", name, err)
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
func RunPostgres(ctx context.Context, name, image, password string, hostPort int) error {
	exists, err := Exists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		if err := RemoveForce(ctx, name); err != nil {
			return fmt.Errorf("remove existing container %s: %w", name, err)
		}
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", name,
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD="+password,
		"-p", fmt.Sprintf("%d:5432", hostPort),
		image,
	)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// WaitReady polls the given TCP address until it accepts connections or
// timeout elapses.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres in container did not become ready within %s", timeout)
}
