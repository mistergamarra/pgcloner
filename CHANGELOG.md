# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- `dump`'s progress indicator now shows a filled bar and percentage
  estimate (`[####----] 42%  3.1MB / ~7.2MB`) based on the summed
  on-disk size of the tables you kept selected in the picker, instead of
  just a raw byte counter with no sense of how far along it is. It's an
  estimate — pg_dump's COPY-format text output doesn't match on-disk
  table size exactly — so it clamps at 100% instead of overshooting.
- `dump`'s permission-denied retry no longer gives up after a fixed 5
  attempts. It now retries indefinitely (excluding one more denied table
  each time), checking in every 3 attempts to ask whether to keep going —
  useful against databases with more than 5 tables the connecting user
  can't access.

### Fixed

- The new dump progress bar could appear completely frozen for the whole
  dump, then suddenly "catch up" at the very end. Cause: the bar made the
  progress line longer than before, and once it exceeded the terminal's
  width it wrapped onto a second row — `\r` only rewinds to the start of
  that wrapped continuation, not the true start of the line, so every
  redraw overlapped garbage instead of overwriting cleanly. It now
  detects terminal width and shrinks/truncates the line to fit instead
  of assuming a wide terminal.
- The progress bar could also sit at `0B / 0%` for the entire dump and
  then fill up in the last second — this one wasn't a display bug:
  `pg_dump` writes nothing while it resolves the schema/dependency graph
  first, a phase that gets slower the more tables get excluded for
  permission-denied errors, so a byte-size-based bar has nothing to show
  yet. It now shows an indeterminate "preparing (no data written yet)"
  spinner during that phase and only switches to the real bar once bytes
  actually start flowing, instead of showing a misleading frozen 0%.

## [0.0.3] - 2026-07-09

### Added

- `restore` can now use [Podman](https://podman.io) instead of Docker —
  set `--container-cmd podman` or `PGCLONER_RESTORE__CONTAINER_CMD=podman`.
  Podman is a verified drop-in for every container command this tool
  issues (`run`, `ps --filter --format`, `inspect --format`, `rm -f`),
  useful if you want to avoid Docker Desktop's commercial licensing.
  `doctor` and the `restore` preflight check now check for whichever
  runtime is configured instead of assuming `docker`.

### Fixed

- `dump` could get permanently stuck when Teleport's suggested bootstrap
  database, or the database the user picked, turned out to be one the
  connecting DB user has no actual grants on (`pg_database` lists every
  database regardless of per-database permissions, so this is expected,
  not a fluke). Both cases now retry instead of aborting the whole
  command:
  - The bootstrap-database heuristic now prompts for an alternate
    bootstrap database name on connect failure and retries.
  - Failing to connect to the database the user picked now returns to
    the database picker (with the same candidate list) instead of
    hard-failing — Esc still cancels the command as before.
- `--version` now omits `commit`/`built` entirely when they're unknown
  (e.g. `go install`-built binaries, which have no local VCS checkout to
  read from) instead of printing `commit none, built unknown`.
- `restore` could fail with `connection reset by peer` right after
  "Starting container ..." on a fresh Postgres container: the readiness
  check only tested raw TCP reachability, which can succeed against the
  official `postgres` image's temporary internal server (used to run init
  scripts on first run) moments before it restarts into its real listening
  process. `pgutil.WaitReady` now retries an actual Postgres protocol ping
  instead, riding out that restart instead of racing it once.

### Changed

- `doctor` and every command's preflight check now point missing-tool
  errors at the README's Prerequisites section instead of printing a
  hardcoded install command (`brew install ...`) per tool — install
  instructions now live in exactly one place instead of two that can
  drift out of sync.

## [0.0.2] - 2026-07-09

### Fixed

- `--version`/`-v` printed `dev (commit none, built unknown)` when
  installed via `go install github.com/mistergamarra/pgcloner/cmd/pgcloner@version`,
  because `-ldflags` (how `.goreleaser.yaml` injects the real version) are
  only applied by GoReleaser's own build step, never by `go install`.
  `main.buildVersion()` now falls back to `runtime/debug.ReadBuildInfo()`
  in that case, which Go stamps automatically with the module version and
  VCS commit/time — so `go install`-built binaries now report the correct
  version too, not just GoReleaser-built release archives.

## [0.0.1] - 2026-07-09

Initial release. A Go CLI for pulling PostgreSQL dumps from Teleport-protected
databases and restoring them into disposable local Docker containers —
nothing in the tool assumes a specific Teleport cluster, DB role naming
scheme, or Docker image.

### Added

- `dump` — interactive wizard: Teleport DB resource → DB user → PostgreSQL
  database → schema → tables → `pg_dump` to a timestamped `.sql` file.
  - Table picker shows on-disk size (KB/MB, largest first), supports
    toggling individual tables, select/deselect-all, and filtering.
  - Automatic permission-denied retry: if the connecting DB user can't
    access some tables, they're excluded and the dump retried (up to 5
    attempts) instead of failing outright.
- `restore` — interactive wizard: pick a `.sql` file → new or existing
  `pgcloner-*` Docker container → confirm → restore.
  - Auto-detects and installs Postgres extensions the dump needs
    (`uuid-ossp`, PostGIS, `hstore`, `citext`, `pg_trgm`, `unaccent`,
    `dict_int`).
  - Strips header statements (`\connect`, `DROP`/`CREATE DATABASE`, `SET
    transaction_timeout`) that would otherwise break the restore.
- `login` / `db-list` — thin wrappers around `tsh login` / `tsh db ls`.
- `doctor` — checks that `tsh`, `pg_dump`, `psql`, and `docker` are
  installed and on `PATH` (plus Docker daemon reachability); every other
  command runs the same check for just what it needs and fails fast with
  an install hint instead of a bare `exec: not found` mid-wizard.
- Ctrl-C cancels cleanly: mid-prompt it just steps back; mid-`pg_dump`/`psql`
  it kills the process immediately, deletes a partial dump file, and exits
  128+SIGINT.
- Full external configurability: every setting (Teleport cluster, DB port,
  DB user list, bootstrap DB, restore image, restore password) is settable
  via CLI flag, `PGCLONER_*` environment variable, or a `.env`
  file next to the binary — flags win, then env, then `.env`, then
  built-in (vendor-neutral) defaults. See `.env.example`.
- `--version`/`-v` prints the build version, commit, and build date.

### Notes

- No Windows support for the interactive commands has been tested yet
  (binaries are built for it, but `tsh`/`docker` behavior on Windows is
  unverified) — see the [README](README.md) for supported platforms.
