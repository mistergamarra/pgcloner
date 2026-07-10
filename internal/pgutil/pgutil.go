// Package pgutil runs the interactive lookup queries (databases, schemas,
// tables) against the Teleport proxy tunnel using pgx, and wraps the
// pg_dump/psql binaries for the actual dump/restore data path — those tools
// remain the authoritative way to move Postgres data.
package pgutil

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mistergamarra/pgcloner/internal/humanize"
)

// ConnString builds a postgres:// URL for the local proxy tunnel.
func ConnString(user string, port int, dbName string) string {
	return fmt.Sprintf("postgres://%s@127.0.0.1:%d/%s?sslmode=disable", user, port, dbName)
}

// ListDatabases returns every non-template database, largest concerns
// (system catalogs) already excluded by the caller if needed.
func ListDatabases(ctx context.Context, connString string) ([]string, error) {
	return queryStrings(ctx, connString,
		`SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname`)
}

// ListSchemas returns every schema in the connected database.
func ListSchemas(ctx context.Context, connString string) ([]string, error) {
	return queryStrings(ctx, connString,
		`SELECT schema_name FROM information_schema.schemata ORDER BY schema_name`)
}

// Table describes one row from Step 5's table picker.
type Table struct {
	Schema    string
	Name      string
	SizeBytes int64
}

// Key returns the "schema.table" form pg_dump's --table/--exclude-table
// flags expect.
func (t Table) Key() string { return t.Schema + "." + t.Name }

// Label returns the human-readable "schema.table (size)" form shown in
// the multi-select list.
func (t Table) Label() string { return fmt.Sprintf("%s (%s)", t.Key(), humanize.Bytes(t.SizeBytes)) }

// ListTables returns every user table, largest first (by on-disk size,
// including indexes/TOAST), optionally filtered to a single schema (empty
// schema means "all schemas except system ones").
func ListTables(ctx context.Context, connString string, schema string) ([]Table, error) {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
		SELECT t.schemaname, t.tablename,
		       pg_total_relation_size(c.oid)
		FROM   pg_tables     t
		JOIN   pg_class     c ON c.relname = t.tablename
		JOIN   pg_namespace n ON n.oid     = c.relnamespace
		                    AND n.nspname  = t.schemaname
		ORDER  BY pg_total_relation_size(c.oid) DESC NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	var out []Table
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.SizeBytes); err != nil {
			return nil, err
		}
		if schema != "" {
			if t.Schema != schema {
				continue
			}
		} else if t.Schema == "pg_catalog" || t.Schema == "information_schema" {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func queryStrings(ctx context.Context, connString, query string) ([]string, error) {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Ping checks that the proxy tunnel is actually speaking Postgres,
// distinct from teleport.Proxy.Wait's raw TCP check.
func Ping(ctx context.Context, connString string) error {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.Ping(ctx)
}
