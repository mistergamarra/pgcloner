// Package doctor checks that the external binaries pgcloner
// shells out to (tsh, pg_dump, psql, docker) are installed, so a missing
// dependency surfaces as one clear message up front instead of an
// "exec: not found" mid-wizard. It only checks — it never installs
// anything.
package doctor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Binary describes one external dependency.
type Binary struct {
	Name string
	// Hint is shown when the binary is missing.
	Hint string
	// VersionArgs, if set, is run to print a one-line version string in
	// the doctor report (e.g. []string{"--version"}).
	VersionArgs []string
	// UsedBy lists the pgcloner commands that need this binary.
	UsedBy []string
}

// Binaries is every external dependency this tool ever shells out to.
var Binaries = []Binary{
	{
		Name:        "tsh",
		Hint:        "install the Teleport client: https://goteleport.com/docs/installation/",
		VersionArgs: []string{"version"},
		UsedBy:      []string{"login", "db-list", "dump"},
	},
	{
		Name:        "pg_dump",
		Hint:        "install PostgreSQL client tools (part of libpq), e.g. `brew install libpq` or `apt install postgresql-client`",
		VersionArgs: []string{"--version"},
		UsedBy:      []string{"dump"},
	},
	{
		Name:        "psql",
		Hint:        "install PostgreSQL client tools (part of libpq), e.g. `brew install libpq` or `apt install postgresql-client`",
		VersionArgs: []string{"--version"},
		UsedBy:      []string{"dump", "restore"},
	},
	{
		Name:        "docker",
		Hint:        "install Docker: https://docs.docker.com/get-docker/",
		VersionArgs: []string{"--version"},
		UsedBy:      []string{"restore"},
	},
}

// Result is one binary's check outcome.
type Result struct {
	Binary  Binary
	Path    string
	Version string
	Err     error
}

// OK reports whether the binary was found on PATH.
func (r Result) OK() bool { return r.Err == nil }

// Check runs PATH lookups (and version probes) for every binary in for.
// A nil/empty for checks everything.
func Check(ctx context.Context, forCommand ...string) []Result {
	results := make([]Result, 0, len(Binaries))
	for _, b := range Binaries {
		if len(forCommand) > 0 && !usedByAny(b, forCommand) {
			continue
		}
		results = append(results, checkOne(ctx, b))
	}
	return results
}

func usedByAny(b Binary, commands []string) bool {
	for _, c := range commands {
		for _, u := range b.UsedBy {
			if u == c {
				return true
			}
		}
	}
	return false
}

func checkOne(ctx context.Context, b Binary) Result {
	path, err := exec.LookPath(b.Name)
	if err != nil {
		return Result{Binary: b, Err: err}
	}
	res := Result{Binary: b, Path: path}
	if len(b.VersionArgs) > 0 {
		out, _ := exec.CommandContext(ctx, b.Name, b.VersionArgs...).CombinedOutput()
		res.Version = firstLine(out)
	}
	return res
}

func firstLine(b []byte) string {
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// Require checks only the binaries used by the given command names and
// returns one combined, actionable error if any are missing.
func Require(ctx context.Context, forCommand ...string) error {
	var missing []string
	for _, r := range Check(ctx, forCommand...) {
		if !r.OK() {
			missing = append(missing, fmt.Sprintf("  - %s: %s", r.Binary.Name, r.Binary.Hint))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required tool(s):\n%s\n\nrun `pgcloner doctor` for a full report",
		strings.Join(missing, "\n"))
}

// Report writes a human-readable ✓/✗ line per binary to w, plus a Docker
// daemon reachability check, and reports whether everything required is
// present (the daemon check is informational only — it doesn't fail the
// overall result, since `restore` fails with its own clear error later).
func Report(ctx context.Context, w io.Writer) (allOK bool) {
	allOK = true
	for _, r := range Check(ctx) {
		if !r.OK() {
			allOK = false
			fmt.Fprintf(w, "✗ %-8s not found — %s\n", r.Binary.Name, r.Binary.Hint)
			continue
		}
		version := r.Version
		if version == "" {
			version = "found"
		}
		fmt.Fprintf(w, "✓ %-8s %s (%s)\n", r.Binary.Name, version, r.Path)
		if r.Binary.Name == "docker" {
			if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
				fmt.Fprintf(w, "  ⚠ docker daemon not reachable — is Docker running?\n")
			}
		}
	}
	return allOK
}
