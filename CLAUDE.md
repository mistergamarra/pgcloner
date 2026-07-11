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
  missing `tsh`/`pg_dump`/`psql`/`docker` fails immediately instead of an
  `exec: not found` mid-wizard. `doctor` itself has no such hook — it's
  the one command allowed to report missing tools rather than fail on
  them.
- **Install instructions live in exactly one place**: the README's
  Prerequisites table. `doctor.go` deliberately has no per-tool install
  command (no `brew install ...`, no OS detection) — every missing-tool
  message (`doctor.readmePointer`) just says "see the Prerequisites
  section in README.md." Keeping install commands in code risks two
  copies drifting out of sync (wrong Homebrew formula name, missing an
  OS, etc.); if the required tools or how to install them change, update
  the README table — `doctor.go` doesn't need touching.

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
- **Container runtime**: `internal/dockerutil.Client` shells out to a
  configurable container CLI binary (`cfg.Restore.ContainerCmd`, default
  `"docker"`, `"podman"` also supported) rather than the Docker SDK,
  keeping the dependency surface small. Podman was added specifically as
  a Docker-Desktop-licensing escape hatch, not a general "support any
  runtime" abstraction — see "Container runtime is docker OR podman,
  nothing lower-level" below for why containerd itself was ruled out.

## Key behaviors to preserve when touching this code

- **`127.0.0.1`, never `localhost`**: macOS resolves `localhost` to `::1`
  first; `tsh proxy db` and Docker's published port both bind IPv4.
- **Proxy readiness**: `teleport.Proxy.Wait` does a raw TCP dial, not a
  Postgres ping — Teleport's tunnel needs a username+DB before it speaks
  the PG protocol, so a `pg_isready`/pgx ping fails against the bare tunnel
  before `db login` completes.
- **Container readiness is the opposite case**: `pgutil.WaitReady`
  (used by `restorecmd` after starting a fresh container) retries a real
  Postgres ping, not a raw TCP dial — here the container's own port *is*
  reachable early, since the official `postgres` image briefly runs a
  temporary internal server (bound to the same port) to execute init
  scripts on first run, then restarts into its real listening process. A
  bare TCP check can succeed against that temporary server and then get
  "connection reset by peer" on the actual protocol handshake moments
  later; retrying the real ping rides out the restart instead of racing
  it once. Don't swap this back to a TCP-only check.
- **Permission-denied retry**: `dumpcmd.runDump` parses `permission denied
  for table <name>` specifically (not `LOCK TABLE`, which lists every
  table being dumped), excludes that table, and retries — unbounded, not
  capped at a fixed attempt count — since this is how dumps succeed when
  the connecting DB user only has read access to some tables. Every
  `permissionDeniedCheckIn` (3) attempts it stops and asks via
  `uiselect.Confirm` whether to keep going instead of retrying (or
  giving up) silently forever; declining returns an error, same as any
  other cancellation path. Don't reintroduce a hard attempt cap here —
  the check-in loop replaced it deliberately, for databases with many
  denied tables where 5 attempts wasn't enough.
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
- **Container runtime is docker OR podman, nothing lower-level**: Docker
  itself already runs on containerd (since 18.09) — the real choice here
  was "shell out to the `docker` CLI vs. containerd's own tooling," not
  "swap runtimes." That was ruled out: containerd's native `ctr` CLI is
  explicitly documented upstream as a debugging tool, not meant for
  scripting, and has no built-in port-publishing (`-p host:5432`) the way
  `docker run`/`podman run` do — supporting it directly would mean
  reimplementing networking ourselves. Podman was added instead because
  it's a verified drop-in for every command this tool issues (`run`,
  `ps --filter --format`, `inspect --format` with the exact same Go
  template, `rm -f`) — see `dockerutil.Client`, which just takes a binary
  name. Don't add a `containerd`/`ctr` code path without first solving
  port publishing for it.
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
- **Database listing vs. connectability**: `dumpcmd.listCandidateDatabases`
  lists databases via `pg_database`, which is visible to any authenticated
  user regardless of that user's actual grants on each individual
  database — so a name showing up in the picker is not a guarantee the
  connecting `dbUser` can actually connect to it. Two places in
  `dumpcmd.Run` handle this rather than hard-failing:
  1. `listDatabasesRetryingBootstrap` — `teleport.Proxy.BootstrapDB()`'s
     suggested bootstrap database is a heuristic (parsed from tsh's
     startup log) and can itself be one the user can't connect to; on
     failure it prompts for a different bootstrap database name and
     retries, rather than aborting `dump` outright.
  2. `Run`'s database-selection loop — if re-auth/connect for the
     *chosen* database fails (Postgres returns "access to db denied" —
     not a Go-distinguishable error, just the connect failing), it loops
     back to the database picker instead of aborting, since the failure
     only means "this particular database," not "this DB user is
     unusable." A cancellation (`uiselect.ErrBack`, e.g. Esc during
     schema selection) is checked for explicitly and still exits
     immediately — only genuine connect failures trigger the retry.
- **Table sizes**: `pgutil.ListTables` selects raw
  `pg_total_relation_size` bytes (not `pg_size_pretty`) and formats them
  via `internal/humanize.Bytes`, which biases toward KB/MB (B and GB only
  show at the extremes) — consistent with the progress indicator's size
  display. Sort order is `pg_total_relation_size(c.oid) DESC NULLS LAST`
  in SQL, so the picker always lists the largest tables first.
- **Dump progress bar is an estimate, not a real percentage**:
  `dumpcmd.Run` sums `SizeBytes` of the tables the user kept selected and
  passes that as `estimatedTotal` into `runDump` → `progress.Watch`. This
  is `pg_total_relation_size` (on-disk, includes indexes/TOAST) being
  compared against the pg_dump COPY-format *text* output size — they
  don't match exactly, so `progress.render` clamps at 100% instead of
  showing e.g. 114% when the dump overshoots the estimate. Don't try to
  make this exact; it's meant to give a rough sense of progress on large
  dumps, not a precise byte count.
- **Progress line must fit the terminal width, or `\r` breaks**:
  `progress.Watch` detects the terminal width once (via
  `charmbracelet/x/term.GetSize`, falling back to 80 columns if that
  fails — redirected output, no TTY, etc.) and `render` shrinks the bar
  (down to `minBarWidth`) or truncates as a last resort to stay within
  it. This isn't cosmetic: if a `\r\033[K`-redrawn line is longer than
  the terminal, it wraps onto a second row, and `\r` only rewinds to the
  start of that wrapped continuation — not the true start of the line —
  so every redraw overlaps garbage instead of overwriting cleanly. The
  visible symptom is the progress line looking completely frozen for the
  whole dump, then "catching up" all at once when a real `\n` (the next
  log line) finally resets the terminal's cursor tracking. If you add
  more to this line, keep it within the shrink/truncate budget rather
  than assuming an 80+ column terminal.
- **The dump progress bar can be legitimately stuck at 0 bytes for a
  long time — that's not a bug**: `pg_dump` builds its full
  schema/dependency/ACL graph before writing a single byte, and that
  phase gets slower the more `--exclude-table` flags are passed (each
  one adds catalog-resolution work) — so with a large schema or many
  permission-denied exclusions, `outfile` can stay at 0 bytes for most
  of the wall-clock time, then fill up fast once the actual data-writing
  phase starts. A byte-size-based bar has nothing to show during that
  first phase. `progress.Watch` tracks whether `sizeFn` has ever reported
  `> 0` (the `started` bool) and renders `renderPreparing` (indeterminate:
  spinner + elapsed only) until it has, switching to the real bar only
  once bytes actually start flowing — don't "fix" this by making the bar
  move fictitiously during that phase; there's nothing real to show yet.
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
