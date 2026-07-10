// Package teleport shells out to the tsh CLI to list DB resources,
// authenticate, and manage the local proxy tunnel — mirroring dump.sh's
// tsh usage but with typed errors instead of scraped stderr.
package teleport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type dbResource struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

// ListDBResources returns every Teleport DB resource name, preferring the
// "Name" label (matches the original `jq -r '.[].metadata.labels.Name //
// .[].metadata.name'`).
func ListDBResources(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "tsh", "db", "ls", "--format=json").Output()
	if err != nil {
		return nil, fmt.Errorf("tsh db ls: %w", asExitErr(err))
	}
	var resources []dbResource
	if err := json.Unmarshal(out, &resources); err != nil {
		return nil, fmt.Errorf("parse tsh db ls output: %w", err)
	}
	names := make([]string, 0, len(resources))
	for _, r := range resources {
		if name := r.Metadata.Labels["Name"]; name != "" {
			names = append(names, name)
			continue
		}
		names = append(names, r.Metadata.Name)
	}
	return names, nil
}

// Login runs `tsh login` against the given cluster, streaming its output
// (it may open a browser and prompt) directly to the terminal.
func Login(ctx context.Context, cluster string) error {
	cmd := exec.CommandContext(ctx, "tsh", "login",
		"--proxy="+cluster+":443", cluster)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// AuthResult carries what LoginDB learned about the caller's access.
type AuthResult struct {
	OK             bool
	AllowedDBNames []string // populated when Teleport policy restricts the db name
	Err            error
}

// LoginDB runs `tsh db login`, classifying the "please provide the
// database name" policy error into a list of allowed names instead of a
// hard failure (dump.sh step 3's fallback path).
func LoginDB(ctx context.Context, dbUser, tunnel, dbName string) AuthResult {
	args := []string{"db", "login", "--db-user=" + dbUser}
	if dbName != "" {
		args = append(args, "--db-name="+dbName)
	}
	args = append(args, tunnel)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "tsh", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		return AuthResult{OK: true}
	}

	msg := stderr.String()
	if strings.Contains(msg, "please provide the database name") {
		return AuthResult{AllowedDBNames: parseAllowedDBNames(msg)}
	}
	return AuthResult{Err: fmt.Errorf("tsh db login: %s", strings.TrimSpace(msg))}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseAllowedDBNames extracts the database names tsh lists inside
// "[...]" in its "please provide the database name" error. It searches
// the whole message rather than a single line, and strips ANSI color
// codes first, because tsh wraps and colors this message even when
// stderr isn't a terminal — a line-by-line, same-line bracket search
// missed the list entirely and fed the picker Fields() of the raw
// message instead.
func parseAllowedDBNames(msg string) []string {
	msg = ansiEscape.ReplaceAllString(msg, "")
	start := strings.Index(msg, "[")
	end := strings.LastIndex(msg, "]")
	if start == -1 || end == -1 || end < start {
		return nil
	}
	raw := msg[start+1 : end]
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.Trim(f, `"' `); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Proxy represents a running `tsh proxy db` tunnel.
type Proxy struct {
	cmd  *exec.Cmd
	port int
	log  bytes.Buffer
}

// StartProxy launches `tsh proxy db --tunnel <tunnel> --port <port>` in the
// background. Call Wait to block until the local port accepts connections,
// and Stop when done.
func StartProxy(ctx context.Context, dbUser, tunnel string, port int, dbName string) (*Proxy, error) {
	args := []string{"proxy", "db", "--db-user=" + dbUser}
	if dbName != "" {
		args = append(args, "--db-name="+dbName)
	}
	args = append(args, "--tunnel", tunnel, "--port", fmt.Sprintf("%d", port))

	p := &Proxy{port: port}
	p.cmd = exec.CommandContext(ctx, "tsh", args...)
	p.cmd.Stdout = &p.log
	p.cmd.Stderr = &p.log
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tsh proxy db: %w", err)
	}
	return p, nil
}

// Wait blocks until the proxy's local port accepts TCP connections, the
// tsh process exits, or timeout elapses. Always uses 127.0.0.1 — macOS
// resolves localhost to ::1 first and tsh only binds IPv4.
func (p *Proxy) Wait(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", p.port)
	for time.Now().Before(deadline) {
		if p.cmd.ProcessState != nil {
			return fmt.Errorf("tsh proxy exited unexpectedly:\n%s", p.log.String())
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("proxy did not become ready within %s:\n%s", timeout, p.log.String())
}

// BootstrapDB scrapes the proxy's startup banner for the "psql
// postgres://user@localhost:PORT/dbname" hint tsh prints, which names a
// database the caller actually has access to.
func (p *Proxy) BootstrapDB() string {
	for _, line := range strings.Split(p.log.String(), "\n") {
		if !strings.Contains(line, "psql postgres://") {
			continue
		}
		idx := strings.LastIndex(line, "/")
		if idx == -1 || idx == len(line)-1 {
			continue
		}
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

// Stop terminates the proxy process, if still running.
func (p *Proxy) Stop() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()
}

func asExitErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%s (%s)", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
