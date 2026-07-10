// Command pgcloner is an interactive CLI for pulling PostgreSQL
// dumps from Teleport-protected databases and restoring them into
// disposable local Docker containers. It has no assumptions about any
// particular Teleport cluster, database role names, or Docker image —
// every setting is configurable via flag, environment variable, or a
// .env file.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/mistergamarra/pgcloner/internal/config"
	"github.com/mistergamarra/pgcloner/internal/doctor"
	"github.com/mistergamarra/pgcloner/internal/dumpcmd"
	"github.com/mistergamarra/pgcloner/internal/restorecmd"
	"github.com/mistergamarra/pgcloner/internal/teleport"
)

const (
	flagCluster    = "teleport-cluster"
	flagDBUsers    = "db-users"
	flagDBPort     = "db-port"
	flagBootstrap  = "bootstrap-db"
	flagPGImage    = "pg-image"
	flagPGPassword = "pg-password"
)

// version, commit, and date are set via -ldflags at build time (see
// .goreleaser.yaml); they stay "dev"/"none"/"unknown" for `go build`/`go
// run` without those flags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: load config:", err)
		os.Exit(1)
	}

	cmd := &cli.Command{
		Name:    "pgcloner",
		Usage:   "dump PostgreSQL databases via Teleport and restore them into local Docker containers",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		Description: "Every flag below can also be set via a PGCLONER_* environment variable\n" +
			"(see README.md) or a .env file next to the binary. Flags take precedence.",
		Suggest: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  flagCluster,
				Value: cfg.Teleport.Cluster,
				Usage: "Teleport proxy address, e.g. teleport.example.com (required for login)",
			},
			&cli.StringFlag{
				Name:  flagDBUsers,
				Value: cfg.Teleport.DBUsersCSV,
				Usage: "comma-separated DB usernames offered in dump's user-selection step (omit to type one in freely)",
			},
			&cli.IntFlag{
				Name:  flagDBPort,
				Value: cfg.Teleport.DBPort,
				Usage: "local port tsh proxy db tunnels through",
			},
			&cli.StringFlag{
				Name:  flagBootstrap,
				Value: cfg.Teleport.Bootstrap,
				Usage: "database used to list other databases when your Teleport role allows any",
			},
			&cli.StringFlag{
				Name:  flagPGImage,
				Value: cfg.Restore.PGImage,
				Usage: "Docker image used for restore containers (any postgres or postgis/postgis tag)",
			},
			&cli.StringFlag{
				Name:  flagPGPassword,
				Value: cfg.Restore.PGPassword,
				Usage: "superuser password set on restore containers",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			applyFlagOverrides(cmd, cfg)
			return ctx, nil
		},
		Commands: []*cli.Command{
			{
				Name:   "login",
				Usage:  "log in to the Teleport cluster",
				Before: requireTools("login"),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if cfg.Teleport.Cluster == "" {
						return fmt.Errorf("no Teleport cluster configured — set --%s or PGCLONER_TELEPORT__CLUSTER", flagCluster)
					}
					return teleport.Login(ctx, cfg.Teleport.Cluster)
				},
			},
			{
				Name:   "db-list",
				Usage:  "list Teleport DB resources in the cluster",
				Before: requireTools("db-list"),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					names, err := teleport.ListDBResources(ctx)
					if err != nil {
						return err
					}
					for _, n := range names {
						fmt.Fprintln(cmd.Root().Writer, n)
					}
					return nil
				},
			},
			{
				Name:   "dump",
				Usage:  "interactive: DB resource -> user -> database -> schema -> tables -> dump",
				Before: requireTools("dump"),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return dumpcmd.Run(ctx, cfg)
				},
			},
			{
				Name:   "restore",
				Usage:  "interactive: pick a .sql dump -> create local DB -> restore",
				Before: requireTools("restore"),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return restorecmd.Run(ctx, cfg)
				},
			},
			{
				Name:  "doctor",
				Usage: "check that tsh, pg_dump, psql, and docker are installed and reachable",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if ok := doctor.Report(ctx, cmd.Root().Writer); !ok {
						return fmt.Errorf("one or more required tools are missing (see above)")
					}
					return nil
				},
			},
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd.Run(ctx, os.Args); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\ncancelled")
			os.Exit(130) // 128 + SIGINT, matching shell convention
		}
		// Printed directly rather than via slog: this message is read by a
		// person at a terminal, and slog's text handler escapes newlines
		// (e.g. doctor's multi-line missing-tool hints), which is unreadable.
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// requireTools returns a Before hook that fails fast with an actionable
// error if any binary the given command name depends on (per
// doctor.Binaries' UsedBy) isn't on PATH — instead of surfacing as a bare
// "exec: not found" partway through an interactive wizard.
func requireTools(commandName string) cli.BeforeFunc {
	return func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		return ctx, doctor.Require(ctx, commandName)
	}
}

// applyFlagOverrides layers explicitly-set global flags on top of the
// config already loaded from env/.env, so flags always win.
func applyFlagOverrides(cmd *cli.Command, cfg *config.AppConf) {
	if cmd.IsSet(flagCluster) {
		cfg.Teleport.Cluster = cmd.String(flagCluster)
	}
	if cmd.IsSet(flagDBUsers) {
		cfg.Teleport.DBUsersCSV = cmd.String(flagDBUsers)
	}
	if cmd.IsSet(flagDBPort) {
		cfg.Teleport.DBPort = int(cmd.Int(flagDBPort))
	}
	if cmd.IsSet(flagBootstrap) {
		cfg.Teleport.Bootstrap = cmd.String(flagBootstrap)
	}
	if cmd.IsSet(flagPGImage) {
		cfg.Restore.PGImage = cmd.String(flagPGImage)
	}
	if cmd.IsSet(flagPGPassword) {
		cfg.Restore.PGPassword = cmd.String(flagPGPassword)
	}
}
