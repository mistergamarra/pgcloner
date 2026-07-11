// Package restorecmd implements `pgcloner restore`: pick a .sql
// dump file, spin up (or reuse) a disposable Postgres/PostGIS Docker
// container, and restore the dump into it.
package restorecmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mistergamarra/pgcloner/internal/config"
	"github.com/mistergamarra/pgcloner/internal/dockerutil"
	"github.com/mistergamarra/pgcloner/internal/pgutil"
	"github.com/mistergamarra/pgcloner/internal/progress"
	"github.com/mistergamarra/pgcloner/internal/uiselect"
)

var dbNameFromFile = regexp.MustCompile(`-\d{14}$`)

// Run drives the restore wizard.
func Run(ctx context.Context, cfg *config.AppConf) error {
	docker := dockerutil.New(cfg.Restore.ContainerCmd)

	dumpfile, err := pickDumpFile()
	if err != nil {
		return err
	}

	containerName, dbName, hostPort, isNew, err := pickTarget(ctx, docker, dumpfile)
	if err != nil {
		return err
	}

	ok, err := uiselect.Confirm(fmt.Sprintf(
		"Restore %s into database %q on container %s (port %d)?",
		dumpfile, dbName, containerName, hostPort))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("aborted")
	}

	if isNew {
		fmt.Fprintf(os.Stderr, "Starting container %s (%s) on port %d...\n", containerName, cfg.Restore.PGImage, hostPort)
		if err := docker.RunPostgres(ctx, containerName, cfg.Restore.PGImage, cfg.Restore.PGPassword, hostPort); err != nil {
			return fmt.Errorf("start container: %w", err)
		}
	}

	maintConn := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/postgres?sslmode=disable", cfg.Restore.PGPassword, hostPort)
	fmt.Fprintln(os.Stderr, "Waiting for Postgres to be ready...")
	if err := pgutil.WaitReady(ctx, maintConn, 30*time.Second); err != nil {
		return fmt.Errorf("postgres not reachable: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Creating database %q...\n", dbName)
	if err := runPsqlCommand(ctx, maintConn, fmt.Sprintf(`CREATE DATABASE "%s";`, dbName)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	conn := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/%s?sslmode=disable", cfg.Restore.PGPassword, hostPort, dbName)
	if err := installExtensions(ctx, dumpfile, conn); err != nil {
		return err
	}
	if err := restoreDump(ctx, dumpfile, conn); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Done.")
	fmt.Printf("  Container : %s\n", containerName)
	fmt.Printf("  Connect   : psql %s\n", conn)
	fmt.Printf("  Stop      : %s stop %s\n", cfg.Restore.ContainerCmd, containerName)
	fmt.Printf("  Remove    : %s rm -f %s\n", cfg.Restore.ContainerCmd, containerName)
	return nil
}

func pickDumpFile() (string, error) {
	matches, err := filepath.Glob("*.sql")
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no .sql files found in current directory")
	}
	sort.Slice(matches, func(i, j int) bool {
		si, _ := os.Stat(matches[i])
		sj, _ := os.Stat(matches[j])
		return si.ModTime().After(sj.ModTime())
	})
	return uiselect.One("Select dump file", matches)
}

// pickTarget lets the user restore into a new container or reuse an
// existing pgcloner-* one, returning the container name, target database
// name, host port, and whether the container still needs to be created.
func pickTarget(ctx context.Context, docker *dockerutil.Client, dumpfile string) (containerName, dbName string, hostPort int, isNew bool, err error) {
	existing, err := docker.ListContainers(ctx)
	if err != nil {
		return "", "", 0, false, err
	}

	const newOption = "New container"
	choices := []string{newOption}
	for _, c := range existing {
		choices = append(choices, fmt.Sprintf("%s (%s)", c.Name, c.Status))
	}
	picked, err := uiselect.One("Select target", choices)
	if err != nil {
		return "", "", 0, false, err
	}

	if picked == newOption {
		defaultName := dbNameFromFile.ReplaceAllString(strings.TrimSuffix(filepath.Base(dumpfile), ".sql"), "")
		dbName, err = uiselect.Input("Database / container name", defaultName)
		if err != nil {
			return "", "", 0, false, err
		}
		port, err := dockerutil.FreePort()
		if err != nil {
			return "", "", 0, false, err
		}
		return "pgcloner-" + dbName, dbName, port, true, nil
	}

	containerName = strings.SplitN(picked, " (", 2)[0]
	dbName = dbNameFromFile.ReplaceAllString(strings.TrimSuffix(filepath.Base(dumpfile), ".sql"), "")
	portStr, err := docker.HostPort(ctx, containerName)
	if err != nil || portStr == "" {
		return "", "", 0, false, fmt.Errorf("could not determine port for %s — is it running?", containerName)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return "", "", 0, false, fmt.Errorf("invalid port %q for %s", portStr, containerName)
	}
	return containerName, dbName, port, false, nil
}

// extensionSignature maps a string found in the dump to the Postgres
// extension it implies (dump.sh's grep-based detection).
var extensionSignatures = []struct {
	pattern *regexp.Regexp
	ext     string
}{
	{regexp.MustCompile(`uuid_generate_v`), "uuid-ossp"},
	{regexp.MustCompile(`ST_|geography|geometry`), "postgis"},
	{regexp.MustCompile(`to_tsvector|plainto_tsquery|tsvector`), "pg_trgm"},
	{regexp.MustCompile(` hstore`), "hstore"},
	{regexp.MustCompile(`citext`), "citext"},
	{regexp.MustCompile(`unaccent`), "unaccent"},
	{regexp.MustCompile(`intdict_template|gpintdict`), "dict_int"},
}

func installExtensions(ctx context.Context, dumpfile, conn string) error {
	f, err := os.Open(dumpfile)
	if err != nil {
		return err
	}
	defer f.Close()

	found := make(map[string]bool)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		for _, sig := range extensionSignatures {
			if !found[sig.ext] && sig.pattern.MatchString(line) {
				found[sig.ext] = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}

	exts := make([]string, 0, len(found))
	for ext := range found {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	fmt.Fprintf(os.Stderr, "Installing extensions: %s\n", strings.Join(exts, " "))
	for _, ext := range exts {
		stmt := fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS "%s";`, ext)
		if err := runPsqlCommand(ctx, conn, stmt); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not install %s (may need a different image)\n", ext)
		}
	}
	return nil
}

// skipLine matches header statements that must be stripped before restore:
// the dump's own DROP/CREATE DATABASE (locale mismatches), \connect (would
// switch psql off the target database), and SET transaction_timeout (not
// recognized by older Postgres versions).
var skipLine = regexp.MustCompile(`^(DROP DATABASE |CREATE DATABASE |ALTER DATABASE .+? OWNER TO |\\connect (?:[^\s]| *$)|SET transaction_timeout )`)

func restoreDump(ctx context.Context, dumpfile, conn string) error {
	src, err := os.Open(dumpfile)
	if err != nil {
		return err
	}
	defer src.Close()

	filtered, err := os.CreateTemp("", "tsh-migration-restore-*.sql")
	if err != nil {
		return err
	}
	defer os.Remove(filtered.Name())
	defer filtered.Close()

	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	w := bufio.NewWriter(filtered)
	for sc.Scan() {
		line := sc.Text()
		if skipLine.MatchString(line) {
			continue
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Restoring %s...\n", dumpfile)
	cmd := exec.CommandContext(ctx, "psql", conn, "--no-password", "--set", "ON_ERROR_STOP=off", "-f", filtered.Name())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start psql restore: %w", err)
	}
	stop := progress.Watch("Restoring", nil, 0)
	err = cmd.Wait()
	stop()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("restore cancelled (target database may be partially populated): %w", ctx.Err())
		}
		return fmt.Errorf("psql restore failed: %w\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "ERROR:") || strings.Contains(stderr.String(), "FATAL:") {
		fmt.Fprintln(os.Stderr, "Warnings during restore:")
		for _, line := range strings.Split(stderr.String(), "\n") {
			if strings.HasPrefix(line, "ERROR:") || strings.HasPrefix(line, "FATAL:") {
				fmt.Fprintln(os.Stderr, line)
			}
		}
	}
	return nil
}

func runPsqlCommand(ctx context.Context, conn, stmt string) error {
	cmd := exec.CommandContext(ctx, "psql", conn, "--no-password", "-c", stmt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
