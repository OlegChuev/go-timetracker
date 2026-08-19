package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oleg/go-timetracker/internal/worklog"
)

// projectColumns is the select list shared by the project queries.
const projectColumns = `id, user_id, name, rate_mode, hourly_rate_cents, monthly_rate_cents, created_at`

// scanProject reads one project row.
func scanProject(row interface{ Scan(...any) error }) (worklog.Project, error) {
	var (
		p         worklog.Project
		mode      string
		hourly    int64
		monthly   int64
		createdAt string
	)
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &mode, &hourly, &monthly, &createdAt); err != nil {
		return worklog.Project{}, err
	}
	p.Rate = worklog.Rate{Mode: mode, HourlyCents: hourly, MonthlyCents: monthly}
	// A malformed timestamp must not sink the whole row; the zero time is fine
	// here because created_at is only ever shown, never compared.
	p.CreatedAt, _ = parseInstant(createdAt)
	return p, nil
}

// CreateProject adds a project with no rate set yet.
func (s *Store) CreateProject(ctx context.Context, userID int64, name string) (worklog.Project, error) {
	const q = `INSERT INTO projects (user_id, name) VALUES (?, ?)`
	res, err := s.db.ExecContext(ctx, q, userID, name)
	if err != nil {
		if isUniqueViolation(err) {
			return worklog.Project{}, worklog.ErrProjectExists
		}
		return worklog.Project{}, fmt.Errorf("create project for %d: %w", userID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return worklog.Project{}, fmt.Errorf("create project for %d: %w", userID, err)
	}
	return s.GetProject(ctx, userID, id)
}

// ListProjects returns a user's projects, oldest first so the order is stable.
func (s *Store) ListProjects(ctx context.Context, userID int64) ([]worklog.Project, error) {
	q := `SELECT ` + projectColumns + ` FROM projects WHERE user_id = ? ORDER BY id`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects for %d: %w", userID, err)
	}
	defer rows.Close()

	var projects []worklog.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("list projects for %d: %w", userID, err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects for %d: %w", userID, err)
	}
	return projects, nil
}

// GetProject loads one project, scoped to its owner so a stale button from
// another chat cannot reach it.
func (s *Store) GetProject(ctx context.Context, userID, projectID int64) (worklog.Project, error) {
	q := `SELECT ` + projectColumns + ` FROM projects WHERE id = ? AND user_id = ?`
	p, err := scanProject(s.db.QueryRowContext(ctx, q, projectID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return worklog.Project{}, worklog.ErrProjectNotFound
	}
	if err != nil {
		return worklog.Project{}, fmt.Errorf("get project %d: %w", projectID, err)
	}
	return p, nil
}

// UpdateProjectRate writes a project's pay rate.
func (s *Store) UpdateProjectRate(ctx context.Context, userID, projectID int64, rate worklog.Rate) error {
	const q = `
		UPDATE projects
		SET rate_mode = ?, hourly_rate_cents = ?, monthly_rate_cents = ?
		WHERE id = ? AND user_id = ?`
	res, err := s.db.ExecContext(ctx, q,
		rate.Mode, rate.HourlyCents, rate.MonthlyCents, projectID, userID)
	if err != nil {
		return fmt.Errorf("update rate for project %d: %w", projectID, err)
	}
	return requireOneRow(res, worklog.ErrProjectNotFound)
}

// RenameProject changes a project's name.
func (s *Store) RenameProject(ctx context.Context, userID, projectID int64, name string) error {
	const q = `UPDATE projects SET name = ? WHERE id = ? AND user_id = ?`
	res, err := s.db.ExecContext(ctx, q, name, projectID, userID)
	if err != nil {
		if isUniqueViolation(err) {
			return worklog.ErrProjectExists
		}
		return fmt.Errorf("rename project %d: %w", projectID, err)
	}
	return requireOneRow(res, worklog.ErrProjectNotFound)
}

// DeleteProject removes a project. Its shifts go with it through the foreign
// key cascade, which is why the caller confirms first.
func (s *Store) DeleteProject(ctx context.Context, userID, projectID int64) error {
	const q = `DELETE FROM projects WHERE id = ? AND user_id = ?`
	res, err := s.db.ExecContext(ctx, q, projectID, userID)
	if err != nil {
		return fmt.Errorf("delete project %d: %w", projectID, err)
	}
	return requireOneRow(res, worklog.ErrProjectNotFound)
}

// CountShifts is how many shifts a project holds, running one included.
func (s *Store) CountShifts(ctx context.Context, userID, projectID int64) (int, error) {
	const q = `SELECT COUNT(*) FROM shifts WHERE user_id = ? AND project_id = ?`
	var n int
	if err := s.db.QueryRowContext(ctx, q, userID, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count shifts for project %d: %w", projectID, err)
	}
	return n, nil
}

// TotalsByProject breaks a period down per project, busiest first. Projects
// with no work in the period are left out.
func (s *Store) TotalsByProject(ctx context.Context, userID int64, from, to time.Time) ([]worklog.ProjectTotals, error) {
	q := `
		SELECT ` + prefixed(projectColumns, "p") + `,
		       COALESCE(SUM(s.duration_seconds), 0),
		       COALESCE(SUM(s.earnings_cents), 0),
		       COUNT(s.id)
		FROM projects p
		JOIN shifts s ON s.project_id = p.id
		WHERE p.user_id = ?
		  AND s.ended_at IS NOT NULL
		  AND s.work_date BETWEEN ? AND ?
		GROUP BY p.id
		ORDER BY SUM(s.duration_seconds) DESC, p.id`
	rows, err := s.db.QueryContext(ctx, q, userID, from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("totals by project for %d: %w", userID, err)
	}
	defer rows.Close()

	var out []worklog.ProjectTotals
	for rows.Next() {
		var (
			p         worklog.Project
			mode      string
			hourly    int64
			monthly   int64
			createdAt string
			totals    worklog.Totals
		)
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &mode, &hourly, &monthly, &createdAt,
			&totals.Seconds, &totals.EarningsCents, &totals.Shifts); err != nil {
			return nil, fmt.Errorf("totals by project for %d: %w", userID, err)
		}
		p.Rate = worklog.Rate{Mode: mode, HourlyCents: hourly, MonthlyCents: monthly}
		p.CreatedAt, _ = parseInstant(createdAt)
		out = append(out, worklog.ProjectTotals{Project: p, Totals: totals})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("totals by project for %d: %w", userID, err)
	}
	return out, nil
}

// ResetUser deletes everything a user has recorded and restores the default
// settings, leaving only the account itself.
func (s *Store) ResetUser(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reset user %d: %w", userID, err)
	}
	defer tx.Rollback()

	// Shifts are removed explicitly rather than relying on the cascade, since
	// a shift can outlive its project only if the schema ever changes.
	for _, q := range []string{
		`DELETE FROM shifts WHERE user_id = ?`,
		`DELETE FROM projects WHERE user_id = ?`,
		`UPDATE users SET currency = 'USD', timezone = 'UTC', pending_action = '' WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return fmt.Errorf("reset user %d: %w", userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reset user %d: %w", userID, err)
	}
	return nil
}

// requireOneRow turns an update that matched nothing into notFound.
func requireOneRow(res sql.Result, notFound error) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound
	}
	return nil
}

// prefixed qualifies a comma separated column list with a table alias.
func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ", ")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
