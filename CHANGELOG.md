# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
