// Package dumpcmd implements `pgcloner dump`: an interactive
// wizard — Teleport DB resource → DB user → Postgres database → schema →
// tables — that ends by running pg_dump against the selected proxy tunnel.
package dumpcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mistergamarra/pgcloner/internal/config"
	"github.com/mistergamarra/pgcloner/internal/humanize"
	"github.com/mistergamarra/pgcloner/internal/pgutil"
	"github.com/mistergamarra/pgcloner/internal/progress"
	"github.com/mistergamarra/pgcloner/internal/teleport"
	"github.com/mistergamarra/pgcloner/internal/uiselect"
)

// Run drives the full dump wizard and writes "<database>-<timestamp>.sql"
// to the current directory.
func Run(ctx context.Context, cfg *config.AppConf) error {
	tunnel, err := pickDBResource(ctx)
	if err != nil {
		return err
	}

	dbUser, err := pickDBUser(cfg)
	if err != nil {
		return err
	}

	dbs, err := listCandidateDatabases(ctx, cfg, dbUser, tunnel)
	if err != nil {
		return err
	}

	// Listing databases only proves they exist (pg_database is visible to
	// any authenticated user); it says nothing about whether dbUser can
	// actually connect to a given one. If re-auth/connect fails for the
	// chosen database, loop back to the picker instead of aborting the
	// whole command — the user may need to try a different one.
	var pgdb, schema, conn string
	var proxy *teleport.Proxy
	for {
		pgdb, err = uiselect.One("Select PostgreSQL database", dbs)
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Re-authenticating %s for %s...\n", dbUser, pgdb)
		if res := teleport.LoginDB(ctx, dbUser, tunnel, pgdb); res.Err != nil {
			return res.Err
		}
		proxy, err = teleport.StartProxy(ctx, dbUser, tunnel, cfg.Teleport.DBPort, pgdb)
		if err != nil {
			return err
		}
		if err = proxy.Wait(30 * time.Second); err != nil {
			proxy.Stop()
			return err
		}
		conn = pgutil.ConnString(dbUser, cfg.Teleport.DBPort, pgdb)

		schema, err = pickSchema(ctx, conn)
		if err == nil {
			break
		}
		proxy.Stop()
		if errors.Is(err, uiselect.ErrBack) {
			return err
		}
		fmt.Fprintf(os.Stderr, "Could not use database %q: %v\n", pgdb, err)
	}
	defer proxy.Stop()

	tables, err := pgutil.ListTables(ctx, conn, schema)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	if len(tables) == 0 {
		return fmt.Errorf("no tables found")
	}
	byLabel := make(map[string]pgutil.Table, len(tables))
	labels := make([]string, len(tables))
	for i, t := range tables {
		labels[i] = t.Label()
		byLabel[t.Label()] = t
	}
	selectedLabels, err := uiselect.Many("Include tables (all pre-selected)", labels)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(selectedLabels))
	for _, l := range selectedLabels {
		selected[byLabel[l].Key()] = true
	}
	var excluded []string
	var estimatedTotal int64
	for _, t := range tables {
		if !selected[t.Key()] {
			excluded = append(excluded, t.Key())
			continue
		}
		estimatedTotal += t.SizeBytes
	}

	outfile := fmt.Sprintf("%s-%s.sql", pgdb, time.Now().Format("20060102150405"))
	if err := runDump(ctx, conn, schema, excluded, tables, outfile, estimatedTotal); err != nil {
		return err
	}

	info, err := os.Stat(outfile)
	if err != nil {
		return err
	}
	fmt.Printf("Done: %s (%s)\n", outfile, humanize.Bytes(info.Size()))
	fmt.Printf("Restore with: pgcloner restore\n")
	return nil
}

// pickDBUser offers a select list built from PGCLONER_TELEPORT__DB_USERS
// when configured, otherwise falls back to a free-text prompt.
func pickDBUser(cfg *config.AppConf) (string, error) {
	users := cfg.Teleport.DBUsers()
	if len(users) == 0 {
		return uiselect.Input("Enter DB user", "")
	}
	return uiselect.One("Select DB user", users)
}

func pickDBResource(ctx context.Context) (string, error) {
	names, err := teleport.ListDBResources(ctx)
	if err != nil {
		return "", fmt.Errorf("list DB resources (run `pgcloner login` first): %w", err)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no Teleport DB resources found — run `pgcloner login`")
	}
	return uiselect.One("Select Teleport DB resource", names)
}

// listCandidateDatabases authenticates against the bootstrap DB (or the
// policy's allowed-DB list) and returns the sorted list of Postgres
// databases dbUser might be able to use. Note this only proves the
// databases exist — pg_database is visible regardless of per-database
// grants — so the caller still needs to handle "picked one we can't
// actually connect to."
func listCandidateDatabases(ctx context.Context, cfg *config.AppConf, dbUser, tunnel string) ([]string, error) {
	fmt.Fprintf(os.Stderr, "Authenticating %s on %s...\n", dbUser, tunnel)
	res := teleport.LoginDB(ctx, dbUser, tunnel, "")

	var dbs []string
	var err error
	switch {
	case res.Err != nil:
		return nil, res.Err
	case res.OK:
		proxy, perr := teleport.StartProxy(ctx, dbUser, tunnel, cfg.Teleport.DBPort, "")
		if perr != nil {
			return nil, perr
		}
		defer proxy.Stop()
		if perr := proxy.Wait(30 * time.Second); perr != nil {
			return nil, perr
		}
		bootDB := proxy.BootstrapDB()
		if bootDB == "" {
			bootDB = cfg.Teleport.Bootstrap
		}
		dbs, err = listDatabasesRetryingBootstrap(ctx, cfg, dbUser, bootDB)
		if err != nil {
			return nil, err
		}
	default:
		fmt.Fprintln(os.Stderr, "  Teleport policy lists allowed databases — using that.")
		dbs = res.AllowedDBNames
	}
	if len(dbs) == 0 {
		return nil, fmt.Errorf("could not list any databases")
	}
	sort.Strings(dbs)
	return dbs, nil
}

// listDatabasesRetryingBootstrap tries bootDB first (tsh's suggested
// database, or the configured fallback). tsh's suggestion is only a
// heuristic — it can name a database the connecting user has no grants
// on, in which case pgx's connect error surfaces as a Postgres "access to
// db denied" error, not a Go error we can distinguish further. Rather
// than hard-failing there with no way forward, prompt for a different
// bootstrap database name and retry; Esc cancels the whole `dump` command
// via uiselect.ErrBack.
func listDatabasesRetryingBootstrap(ctx context.Context, cfg *config.AppConf, dbUser, bootDB string) ([]string, error) {
	for {
		conn := pgutil.ConnString(dbUser, cfg.Teleport.DBPort, bootDB)
		dbs, err := pgutil.ListDatabases(ctx, conn)
		if err == nil {
			return dbs, nil
		}
		fmt.Fprintf(os.Stderr, "Could not list databases via bootstrap %q: %v\n", bootDB, err)
		bootDB, err = uiselect.Input("Enter a different bootstrap database to try (Esc to cancel)", cfg.Teleport.Bootstrap)
		if err != nil {
			return nil, err
		}
	}
}

func pickSchema(ctx context.Context, conn string) (string, error) {
	schemas, err := pgutil.ListSchemas(ctx, conn)
	if err != nil {
		return "", fmt.Errorf("list schemas: %w", err)
	}
	if len(schemas) == 0 {
		return "", fmt.Errorf("could not fetch schemas")
	}
	choices := append([]string{"(all schemas)"}, schemas...)
	picked, err := uiselect.One("Select schema", choices)
	if err != nil {
		return "", err
	}
	if picked == "(all schemas)" {
		return "", nil
	}
	return picked, nil
}

// permissionDeniedCheckIn is how many consecutive permission-denied
// retries runDump allows before checking in with the user instead of
// retrying silently forever.
const permissionDeniedCheckIn = 3

// runDump invokes pg_dump, excluding one more permission-denied table
// each time it fails on one — this lets a read-limited DB user dump
// everything it can access instead of failing outright. Every
// permissionDeniedCheckIn attempts, it stops and asks whether to keep
// going rather than retrying (or giving up) silently.
func runDump(ctx context.Context, conn, schema string, excluded []string, tables []pgutil.Table, outfile string, estimatedTotal int64) error {
	deniedPattern := regexp.MustCompile(`permission denied for table ([a-z_]+)`)
	keyBySuffix := make(map[string]string, len(tables))
	for _, t := range tables {
		keyBySuffix[t.Name] = t.Key()
	}

	for attempt := 1; ; attempt++ {
		args := []string{conn, "--no-password", "--format=plain", "--no-owner", "--no-acl",
			"--create", "--clean", "--if-exists"}
		if schema != "" {
			args = append(args, "--schema="+schema)
		}
		for _, e := range excluded {
			args = append(args, "--exclude-table="+e)
		}
		args = append(args, "--file="+outfile)

		cmd := exec.CommandContext(ctx, "pg_dump", args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start pg_dump: %w", err)
		}
		stop := progress.Watch(fmt.Sprintf("Dumping (attempt %d)", attempt), fileSize(outfile), estimatedTotal)
		err := cmd.Wait()
		stop()
		if err == nil {
			return validateDump(outfile)
		}
		if ctx.Err() != nil {
			_ = os.Remove(outfile)
			return fmt.Errorf("dump cancelled: %w", ctx.Err())
		}

		matches := deniedPattern.FindAllStringSubmatch(stderr.String(), -1)
		if len(matches) == 0 {
			return fmt.Errorf("pg_dump failed: %s", stderr.String())
		}
		seen := make(map[string]bool)
		for _, m := range matches {
			seen[m[1]] = true
		}
		var newlyDenied []string
		for name := range seen {
			newlyDenied = append(newlyDenied, name)
			if key, ok := keyBySuffix[name]; ok {
				excluded = append(excluded, key)
			}
		}
		fmt.Fprintf(os.Stderr, "  Excluding (permission denied): %s\n", strings.Join(newlyDenied, " "))

		if attempt%permissionDeniedCheckIn == 0 {
			cont, cerr := uiselect.Confirm(fmt.Sprintf(
				"Still hitting permission-denied errors after %d attempts (%d tables excluded so far) — keep retrying?",
				attempt, len(excluded)))
			if cerr != nil {
				return cerr
			}
			if !cont {
				return fmt.Errorf("dump cancelled after %d permission-denied retries", attempt)
			}
		}
	}
}

func validateDump(outfile string) error {
	info, err := os.Stat(outfile)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("dump file is empty — check connection and table permissions")
	}
	f, err := os.Open(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "CREATE TABLE") || strings.HasPrefix(line, "COPY ") {
			return nil
		}
	}
	return fmt.Errorf("dump has no table data (only preamble); the selected tables may have no rows or SELECT access")
}

func fileSize(path string) func() int64 {
	return func() int64 {
		info, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return info.Size()
	}
}
