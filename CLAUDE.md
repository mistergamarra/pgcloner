# CLAUDE.md

Guidance for working in this repo.

## What this is

A general-purpose Go CLI: `urfave/cli/v3` command tree, `koanf`-based env
config layered with CLI flags, one `internal/` package per concern. The
binary's entry point lives at `cmd/pgcloner/main.go` (standard Go
project layout — `internal/` packages, not `main.go`, hold all the logic).

Nothing in this repo assumes a specific Teleport cluster, DB role naming
scheme, or Docker image — see `internal/config` and `cmd/pgcloner/main.go`
for the full set of flags/env vars, all with vendor-neutral defaults (or
no default, where a value must be supplied).

Module path is `github.com/mistergamarra/pgcloner` — it must
match the actual GitHub repo path, since `go install
github.com/mistergamarra/pgcloner/cmd/pgcloner@latest`
(the README's primary install method) only resolves correctly when it
does. If this repo is ever forked/renamed, update `module` in `go.mod`,
every `github.com/mistergamarra/pgcloner/internal/...` import,
`.goreleaser.yaml`'s `release.github.owner`/`name`, and the README's
install commands and badge URLs together — they'll silently drift
otherwise.

## Conventions

- **CLI framework**: `github.com/urfave/cli/v3`, `Suggest: true` on the
  root command. Global flags mirror every `config.AppConf` field so users
  never have to look up an env var name to override a setting.
- **Config**: `koanf` loaded from `PGCLONER_*` env vars via
  `internal/config`, `.env` auto-loaded next to the binary via `godotenv`.
  CLI flags are layered on top in `main.applyFlagOverrides` and always win.
- **Errors**: every command returns an `error` from its `Action` (never
  calls `os.Exit` itself); `main` prints it directly to stderr with
  `fmt.Fprintln` — not `log/slog` — since these messages are read by a
  human at a terminal, and slog's text handler escapes newlines (breaking
  `doctor`'s multi-line missing-tool hints).
- **Preflight checks**: `internal/doctor` knows which external binary each
  command needs (`doctor.Binaries[].UsedBy`). Every command that shells
  out gets a `Before: requireTools("<name>")` hook in `main.go`, so a
  missing `tsh`/`pg_dump`/`psql`/`docker` fails immediately with an
  actionable hint instead of an `exec: not found` mid-wizard. `doctor`
  itself has no such hook — it's the one command allowed to report
  missing tools rather than fail on them.

## Deviations from the bash scripts (and why)

- **Interactive selection**: `github.com/charmbracelet/huh` replaces the
  bash scripts' hand-rolled numbered-menu + `fzf` combo (`internal/uiselect`).
  This drops the `fzf` binary dependency entirely — `huh`'s multi-select
  already supports filtering and toggle-all.
- **Postgres queries**: `internal/pgutil` uses `jackc/pgx/v5` directly for
  the interactive lookups (list databases/schemas/tables), instead of
  shelling out to `psql -t -A`. `pg_dump`/`psql` are still invoked via
  `os/exec` for the actual dump and restore — those remain the
  authoritative tools for moving data.
- **Docker**: `internal/dockerutil` shells out to the `docker` CLI rather
  than the Docker SDK, matching the original scripts' approach and keeping
  the dependency surface small.

## Key behaviors to preserve when touching this code

- **`127.0.0.1`, never `localhost`**: macOS resolves `localhost` to `::1`
  first; `tsh proxy db` and Docker's published port both bind IPv4.
- **Proxy readiness**: `teleport.Proxy.Wait` does a raw TCP dial, not a
  Postgres ping — Teleport's tunnel needs a username+DB before it speaks
  the PG protocol, so a `pg_isready`/pgx ping fails against the bare tunnel
  before `db login` completes.
- **Permission-denied retry**: `dumpcmd.runDump` parses `permission denied
  for table <name>` specifically (not `LOCK TABLE`, which lists every
  table being dumped) and retries up to 5 times, excluding one more table
  each attempt. This is how dumps succeed even when the connecting DB user
  only has read access to some tables.
- **`--exclude-table` blacklist, not `--table` whitelist**: a whitelist
  drops shared types, sequences, and extensions from the dump.
- **COPY format, not `--inserts`**: large JSON columns with embedded
  newlines break `--inserts`' dollar-quoting; default COPY format escapes
  them correctly.
- **Restore line filtering**: `restorecmd.restoreDump` strips
  `DROP/CREATE DATABASE`, `ALTER DATABASE ... OWNER TO`, `\connect`, and
  `SET transaction_timeout` before feeding the dump to `psql -f`, using a
  buffered `bufio.Scanner` with a large max-token size (not a fixed-width
  buffer) so very long lines (big JSON blobs) don't get truncated.
- **Container naming**: always `pgcloner-<dbname>`; an existing container
  with that name is removed before a new one is created.
- **Ctrl-C cancellation**: `main` wraps the root context in
  `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`, so every
  `exec.CommandContext`-based call (`pg_dump`, `psql`, `tsh`) is killed the
  moment the user hits Ctrl-C. `dumpcmd.runDump` and
  `restorecmd.restoreDump` both check `ctx.Err()` after a failed
  `cmd.Wait()` to distinguish "user cancelled" from a real command
  failure; the dump path also deletes the partial `.sql` file on
  cancellation. `main` maps `errors.Is(err, context.Canceled)` to a plain
  "cancelled" message and exit code 130, instead of the generic
  `Error: <err>` path. Interactive `huh` prompts are unaffected by this — huh
  puts the terminal in raw mode and intercepts Ctrl-C itself
  (`uiselect.ErrBack`), it never reaches the OS as SIGINT.
- **Table sizes**: `pgutil.ListTables` selects raw
  `pg_total_relation_size` bytes (not `pg_size_pretty`) and formats them
  via `internal/humanize.Bytes`, which biases toward KB/MB (B and GB only
  show at the extremes) — consistent with the progress indicator's size
  display. Sort order is `pg_total_relation_size(c.oid) DESC NULLS LAST`
  in SQL, so the picker always lists the largest tables first.
- **Multi-select controls**: `uiselect.Many`'s huh MultiSelect ships with
  `space`/`x` to toggle one row and `ctrl+a` to toggle all — both are huh
  defaults, not something this code implements. The `Description()` set
  in `uiselect.Many` just surfaces those bindings in the prompt so they
  aren't hidden in huh's footer.

## Versioning and releases

- **Version embedding**: `main.version`/`main.commit`/`main.date` are
  package-level vars, `"dev"`/`"none"`/`"unknown"` by default. `.goreleaser.yaml`
  injects real values via `-ldflags -X main.version=... -X main.commit=... -X main.date=...`
  at build time. Wired to `cli.Command.Version`, which gives `--version`/`-v`
  for free — don't hand-roll a version flag.
- **Release pipeline**: `.github/workflows/release.yml` runs
  `goreleaser release --clean` on every `v*` tag push, cross-compiling for
  darwin/linux/windows × amd64/arm64 (windows/arm64 excluded — see
  `.goreleaser.yaml`'s `builds[].ignore`), producing `.tar.gz`/`.zip`
  archives, a `checksums.txt`, and a GitHub Release with all of that
  attached. `.github/workflows/ci.yml` runs two jobs on every push and
  PR: `build` (`go build`/`go vet`/`go test`/`gofmt -l`, matrixed across
  ubuntu/macos) and `vulncheck` (`govulncheck ./...`) — keep both green
  before tagging.
- **`go.mod`'s `go` directive tracks the latest patch release**, not just
  the minor version — bumped from 1.26.2 to 1.26.5 after `govulncheck`
  flagged three *stdlib* vulnerabilities (TLS ECH privacy leak, x509
  hostname parsing, a Windows `net.Dial` panic) that were only fixed in
  later 1.26.x patches. `actions/setup-go` uses `go-version-file: go.mod`,
  so CI always builds with whatever patch is pinned here — if
  `govulncheck` ever flags a stdlib CVE again, the fix is bumping this
  directive (and the local toolchain, via `brew upgrade go` or equivalent)
  to a patched version, not just chasing dependency versions.
- **Dependency updates**: `.github/dependabot.yml` opens weekly PRs for
  both `gomod` (direct + indirect Go deps) and `github-actions` (pinned
  action versions in the two workflows) ecosystems. CI + govulncheck run
  on those PRs same as any other, so a bump that breaks the build or
  reintroduces a known vuln is caught before merge.
- **Before tagging a release**: update `CHANGELOG.md` first (Keep a
  Changelog style) — GoReleaser's own commit-based changelog is enabled
  too (see `changelog:` in `.goreleaser.yaml`) but is a weak substitute
  for a hand-written entry when a release bundles many small commits.
- **Local dry run**: `goreleaser release --snapshot --clean --skip=publish`
  builds everything into `dist/` without needing a tag or pushing
  anywhere — use this to validate `.goreleaser.yaml` changes.

## Build / run / test

```sh
go build ./...
go vet ./...
go run ./cmd/pgcloner dump
go run ./cmd/pgcloner restore
```

`internal/teleport/teleport_test.go` covers `parseAllowedDBNames` with
table-driven cases (including ANSI-colored, line-wrapped tsh output — the
bug that motivated the test). The rest of the CLI is interactive and
mostly untested; parsing/filtering logic elsewhere (`pg_dump`/`psql`
output, dump-restore line filtering) is a good candidate for the same
pattern as the project grows.
