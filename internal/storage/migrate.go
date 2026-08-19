package storage

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
)

// createMigrationsTable records which migration files have already run.
const createMigrationsTable = `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	)`

// Migrate applies every .sql file in files that has not run yet, in filename
// order. Each file runs in its own transaction and is recorded on success, so
// a failure part way through leaves the database on the last good migration.
func (s *Store) Migrate(ctx context.Context, files fs.FS) error {
	if _, err := s.db.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	if len(names) == 0 {
		return fmt.Errorf("no migrations found")
	}
	sort.Strings(names)

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		statements, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.applyMigration(ctx, name, string(statements)); err != nil {
			return err
		}
	}
	return nil
}

// appliedMigrations reads the set of migration names already recorded.
func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration file and records it, both or neither.
func (s *Store) applyMigration(ctx context.Context, name, statements string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, statements); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	return nil
}
