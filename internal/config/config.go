// Package config loads pgcloner settings from environment
// variables (PGCLONER_* by default), with sane, vendor-neutral
// defaults. Every value can also be overridden via CLI flags — see
// cmd/pgcloner/main.go.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

const envPrefix = "PGCLONER_"

// AppConf holds every tunable for the dump and restore commands. None of
// the defaults point at a real cluster, host, or credential — this is a
// generic tool with no assumptions about your Teleport setup.
type AppConf struct {
	Teleport TeleportConf `koanf:"teleport"`
	Restore  RestoreConf  `koanf:"restore"`
}

type TeleportConf struct {
	// Cluster is the Teleport proxy address used by `login`, e.g.
	// "teleport.example.com". Left empty by default — `login` requires
	// it to be set via flag or env var.
	Cluster string `koanf:"cluster"`
	// DBPort is the local port `tsh proxy db` tunnels through.
	DBPort int `koanf:"db_port"`
	// DBUsersCSV is a comma-separated list of DB usernames offered in the
	// dump wizard's "select DB user" step, e.g. "admin,readonly". Empty by
	// default: the wizard falls back to a free-text prompt when unset.
	DBUsersCSV string `koanf:"db_users"`
	// Bootstrap is the database used to list other databases when the
	// caller's Teleport role doesn't restrict them to a specific list.
	Bootstrap string `koanf:"bootstrap_db"`
}

// DBUsers splits DBUsersCSV into a trimmed, non-empty slice.
func (t TeleportConf) DBUsers() []string {
	if t.DBUsersCSV == "" {
		return nil
	}
	parts := strings.Split(t.DBUsersCSV, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type RestoreConf struct {
	// PGImage is the Docker image used for restore containers. Any
	// postgres or postgis/postgis tag works.
	PGImage string `koanf:"pg_image"`
	// PGPassword is the superuser password set on restore containers.
	PGPassword string `koanf:"pg_password"`
}

func defaults() *AppConf {
	return &AppConf{
		Teleport: TeleportConf{
			DBPort:    10007,
			Bootstrap: "postgres",
		},
		Restore: RestoreConf{
			PGImage:    "postgres:16",
			PGPassword: "postgres",
		},
	}
}

// Load reads a .env file (if present) and then layers PGCLONER_*
// environment variables on top of the defaults. Nested keys use a double
// underscore, e.g. PGCLONER_TELEPORT__DB_PORT, so it doesn't
// collide with underscores already present in field names like db_users.
func Load() (*AppConf, error) {
	loadDotEnv()

	k := koanf.New(".")
	cfg := defaults()
	if err := k.Load(structs.Provider(cfg, "koanf"), nil); err != nil {
		return nil, err
	}
	if err := k.Load(env.Provider(envPrefix, ".", func(s string) string {
		s = strings.TrimPrefix(s, envPrefix)
		return strings.ToLower(strings.ReplaceAll(s, "__", "."))
	}), nil); err != nil {
		return nil, err
	}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDotEnv loads a .env file next to the running binary first (so the
// tool behaves the same regardless of the caller's working directory),
// then the current working directory as a fallback (convenient for
// `go run`, where the binary lives in a temp dir). godotenv.Load never
// overrides a variable already present in the environment, so whichever
// is loaded first wins if both define the same key.
func loadDotEnv() {
	if exe, err := os.Executable(); err == nil {
		_ = godotenv.Load(filepath.Join(filepath.Dir(exe), ".env"))
	}
	_ = godotenv.Load()
}
