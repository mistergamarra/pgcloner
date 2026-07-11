# pgcloner

[![CI](https://github.com/mistergamarra/pgcloner/actions/workflows/ci.yml/badge.svg)](https://github.com/mistergamarra/pgcloner/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/mistergamarra/pgcloner)](https://github.com/mistergamarra/pgcloner/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Interactive Go CLI for pulling PostgreSQL dumps from remote databases via
[Teleport](https://goteleport.com/) and restoring them into isolated local
containers (Docker or Podman). Nothing in the tool is tied to a particular
Teleport cluster, database role naming scheme, container runtime, or
image — every setting is configurable via flag, environment variable, or
`.env` file.

## Prerequisites

External binaries on `PATH`:

| Tool | Purpose | Install |
|------|---------|---------|
| `tsh` (Teleport) | Authenticate and proxy Teleport DB connections | `brew install teleport` (macOS) or see [goteleport.com/docs/installation](https://goteleport.com/docs/installation/) |
| `pg_dump` / `psql` (libpq) | Run the actual dump and restore | `brew install libpq && brew link --force libpq` (macOS) or `apt install postgresql-client` (Debian/Ubuntu) |
| `docker` **or** `podman` | Run isolated restore containers | Docker: see [docs.docker.com/get-docker](https://docs.docker.com/get-docker/). Podman: `brew install podman && podman machine init && podman machine start` (macOS) or see [podman.io/docs/installation](https://podman.io/docs/installation) — set `--container-cmd podman` (or `PGCLONER_RESTORE__CONTAINER_CMD=podman`) to use it |

Podman is a fully open-source, daemonless drop-in for everything `restore`
does with Docker (`run`/`ps`/`inspect`/`rm`) — useful if you want to avoid
Docker Desktop's commercial licensing terms. Docker remains the default.

Go dependencies (`go.mod`) are fetched automatically by `go build`/`go run`.
Run `pgcloner doctor` after building to check all of the above are
installed and on `PATH` — every other command also runs this check for
just what it needs and fails fast if something's missing, pointing back
to this table instead of a bare `exec: not found` mid-wizard.

## Install

**Option 1 — download a prebuilt binary** (no Go toolchain needed):

Grab the archive for your OS/arch from the
[latest release](https://github.com/mistergamarra/pgcloner/releases/latest),
verify it against the release's `checksums.txt`, then install it globally:

```sh
# macOS (Apple Silicon) example — swap in the file for your OS/arch
curl -LO https://github.com/mistergamarra/pgcloner/releases/latest/download/pgcloner_<version>_darwin_arm64.tar.gz
tar -xzf pgcloner_<version>_darwin_arm64.tar.gz pgcloner
sudo install -m 0755 pgcloner /usr/local/bin/pgcloner
pgcloner --version
```

**Option 2 — `go install`** (requires Go 1.26.5+):

```sh
go install github.com/mistergamarra/pgcloner/cmd/pgcloner@latest
# binary lands in $(go env GOPATH)/bin — make sure that's on your PATH
pgcloner --version
```

**Option 3 — build from source:**

```sh
git clone https://github.com/mistergamarra/pgcloner.git
cd pgcloner
go build -o bin/pgcloner ./cmd/pgcloner
```

## Usage

The examples below assume `pgcloner` is on your `PATH` (Options 1
and 2 above). If you built from source without installing it, replace
`pgcloner` with `./bin/pgcloner`.

```sh
pgcloner doctor     # check tsh/pg_dump/psql/docker are installed
pgcloner --teleport-cluster teleport.example.com login
pgcloner db-list    # list Teleport DB resources
pgcloner dump       # interactive: resource -> user -> db -> schema -> tables -> .sql file
pgcloner restore    # interactive: pick .sql -> new/existing container -> restore
```

Run `pgcloner --help` for the full flag list; every flag has a
matching environment variable (see [Configuration](#configuration) below).

### Key interactive features

| Feature | How |
|---------|-----|
| **Cancel anytime** | Ctrl-C during a prompt backs up a step; Ctrl-C during `pg_dump`/`psql` kills the process immediately. A partially-written `dump` output file is deleted automatically; a cancelled `restore` may leave the target database partially populated. |
| **Unselect tables** | In the table picker, press `space` or `x` to toggle the highlighted table off/on |
| **Select/deselect all tables** | Press `ctrl+a` to toggle every table at once |
| **Filter long table lists** | Press `/`, type to narrow the list by name, then press **Enter to leave the filter box** — `space`/`x`/`ctrl+a` only toggle selections once you're back in list-navigation mode. While the filter box itself is active, `space`/`x` just type into it. Press Enter again afterward to submit the whole picker. |
| **Table sizes at a glance** | Each table shows its on-disk size in KB/MB, sorted largest-first |
| **Free-text DB user** | If `--db-users`/`PGCLONER_TELEPORT__DB_USERS` isn't set, `dump` prompts for a DB user as free text instead of a list |

`dump` walks through:

1. Select a Teleport DB resource
2. Select (or type) a DB user
3. Select a PostgreSQL database
4. Select a schema (or all)
5. Select tables to include (multi-select, all pre-selected — see the
   table above for the picker controls)

Output is `<database>-<timestamp>.sql` in the current directory. Tables the
connecting user can't access are excluded automatically via a
permission-denied retry loop (up to 5 attempts) — useful when your
Teleport role only grants read access to some tables.

`restore` walks through:

1. Pick a `.sql` file
2. Choose target — a **new** container or an **existing** `pgcloner-*` one
3. Name the database (new container only)
4. Confirm

It then starts (or reuses) a Postgres container, creates the target
database, auto-installs any extensions the dump requires (`uuid-ossp`,
PostGIS, `hstore`, `citext`, `pg_trgm`, `unaccent`, `dict_int`), and
restores the dump — stripping header statements (`\connect`,
`DROP/CREATE DATABASE`, `SET transaction_timeout`) that would otherwise
break the restore. Use a `postgis/postgis:*` image (via `--pg-image`) if
your dump needs PostGIS.

## Configuration

Every setting can be set three ways, in order of precedence:

1. CLI flag (e.g. `--teleport-cluster teleport.example.com`)
2. Environment variable, prefixed `PGCLONER_` (nested keys use a
   double underscore, e.g. `PGCLONER_TELEPORT__DB_PORT`)
3. A `.env` file next to the binary (loaded automatically) — copy
   [`.env.example`](.env.example) to `.env` and fill in what you need

| Flag | Env var | Default | Notes |
|------|---------|---------|-------|
| `--teleport-cluster` | `PGCLONER_TELEPORT__CLUSTER` | *(none)* | required for `login`, e.g. `teleport.example.com` |
| `--db-port` | `PGCLONER_TELEPORT__DB_PORT` | `10007` | local proxy port |
| `--db-users` | `PGCLONER_TELEPORT__DB_USERS` | *(none)* | comma-separated list shown in the user-selection step; omit to type one freely |
| `--bootstrap-db` | `PGCLONER_TELEPORT__BOOTSTRAP_DB` | `postgres` | fallback DB for listing other databases |
| `--container-cmd` | `PGCLONER_RESTORE__CONTAINER_CMD` | `docker` | `docker` or `podman` |
| `--pg-image` | `PGCLONER_RESTORE__PG_IMAGE` | `postgres:16` | any `postgres` or `postgis/postgis` tag |
| `--pg-password` | `PGCLONER_RESTORE__PG_PASSWORD` | `postgres` | superuser password for the local container |

## Project layout

```
pgcloner/
├── .github/
│   ├── dependabot.yml             # weekly PRs for go.mod + GitHub Actions dependency updates
│   └── workflows/
│       ├── ci.yml                 # build/vet/test/gofmt + govulncheck on push + PR
│       └── release.yml            # GoReleaser on every v*.*.* tag push
├── .goreleaser.yaml               # cross-platform build/archive/checksum/release config
├── cmd/pgcloner/
│   └── main.go                   # urfave/cli/v3 command tree + flags (minimal — wires internal packages)
├── internal/
│   ├── config/                   # koanf env-based config (AppConf)
│   ├── doctor/                   # checks tsh/pg_dump/psql/docker are installed (used by `doctor` + preflight)
│   ├── uiselect/                 # charmbracelet/huh select/multi-select/confirm/input wrappers
│   ├── teleport/                 # tsh db ls / login / proxy tunnel management
│   ├── pgutil/                   # pgx queries (databases/schemas/tables) + connection strings
│   ├── dockerutil/               # docker CLI wrapper (containers, ports)
│   ├── humanize/                 # shared byte-count formatting (KB/MB)
│   ├── progress/                 # stderr progress indicator for long-running pg_dump/psql
│   ├── dumpcmd/                  # dump wizard + pg_dump orchestration
│   └── restorecmd/               # restore wizard + psql restore orchestration
├── CHANGELOG.md
├── LICENSE
└── go.mod
```

## Managing containers

Replace `docker` with `podman` below if that's what you configured.

```sh
docker ps -a --filter name=pgcloner-
psql "postgres://postgres:postgres@127.0.0.1:<port>/<dbname>"
docker stop pgcloner-<dbname>
docker rm -f pgcloner-<dbname>
```

## Releasing (maintainers)

Releases are built and published automatically by
[GoReleaser](https://goreleaser.com) via
`.github/workflows/release.yml`, triggered by pushing a semver tag:

```sh
# 1. update CHANGELOG.md with the new version's notes
# 2. commit that, then tag and push
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions then cross-compiles for macOS/Linux/Windows (amd64 + arm64),
generates checksums, and publishes a GitHub Release with the archives
attached. To dry-run the whole pipeline locally before tagging:

```sh
goreleaser release --snapshot --clean --skip=publish
```

## License

[MIT](LICENSE)
