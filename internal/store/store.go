// Package store is dispatch's canonical task registry, backed by SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/matthewalunni/dispatch/internal/roles"
)

// ErrNotFound is returned when no task matches a reference.
var ErrNotFound = errors.New("task not found")

// AmbiguousError is returned when a reference matches several tasks. Dispatch
// reports the ambiguity rather than guessing.
type AmbiguousError struct {
	Ref     string
	Matches []Task
}

func (e *AmbiguousError) Error() string {
	names := make([]string, 0, len(e.Matches))
	for _, t := range e.Matches {
		names = append(names, fmt.Sprintf("%s (%s)", t.Slug, t.ShortID()))
	}
	return fmt.Sprintf("%q matches %d tasks: %s", e.Ref, len(e.Matches), strings.Join(names, ", "))
}

// Store owns the SQLite database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the task database and applies migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data directory %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// SQLite is happiest with a single writer; dispatch is not throughput bound.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Path is the database file location.
func (s *Store) Path() string { return s.path }

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// migration is one ordered, idempotent schema step. There is no migration
// framework here on purpose, but schema evolution is not brittle either: add a
// step to the end of the slice and every existing database picks it up.
type migration struct {
	name string
	stmt string
}

var migrations = []migration{
	{
		name: "0001_tasks",
		stmt: `
CREATE TABLE IF NOT EXISTS tasks (
    id                 TEXT PRIMARY KEY,
    title              TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    slug               TEXT NOT NULL DEFAULT '',
    project_root       TEXT NOT NULL DEFAULT '',
    project_name       TEXT NOT NULL DEFAULT '',
    role               TEXT NOT NULL DEFAULT '',
    runtime            TEXT NOT NULL DEFAULT '',
    isolation          TEXT NOT NULL DEFAULT 'none',
    status             TEXT NOT NULL DEFAULT 'pending',
    worktree           TEXT NOT NULL DEFAULT '',
    branch             TEXT NOT NULL DEFAULT '',
    base_ref           TEXT NOT NULL DEFAULT '',
    herdr_agent        TEXT NOT NULL DEFAULT '',
    herdr_workspace_id TEXT NOT NULL DEFAULT '',
    herdr_tab_id       TEXT NOT NULL DEFAULT '',
    herdr_pane_id      TEXT NOT NULL DEFAULT '',
    herdr_session      TEXT NOT NULL DEFAULT '',
    parent_task_id     TEXT NOT NULL DEFAULT '',
    error              TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    completed_at       TEXT
);
CREATE INDEX IF NOT EXISTS idx_tasks_status       ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project      ON tasks(project_root);
CREATE INDEX IF NOT EXISTS idx_tasks_created      ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_parent       ON tasks(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_tasks_slug         ON tasks(slug);
CREATE INDEX IF NOT EXISTS idx_tasks_herdr_agent  ON tasks(herdr_agent);
`,
	},
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        name       TEXT PRIMARY KEY,
        applied_at TEXT NOT NULL
    );`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	for _, m := range migrations {
		var seen int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, m.name).Scan(&seen); err != nil {
			return err
		}
		if seen > 0 {
			continue
		}
		if _, err := s.db.ExecContext(ctx, m.stmt); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, m.name, nowString()); err != nil {
			return err
		}
	}
	return nil
}

const taskColumns = `id, title, description, slug, project_root, project_name, role, runtime,
    isolation, status, worktree, branch, base_ref, herdr_agent, herdr_workspace_id,
    herdr_tab_id, herdr_pane_id, herdr_session, parent_task_id, error,
    created_at, updated_at, completed_at`

// Create inserts a new task.
func (s *Store) Create(ctx context.Context, t *Task) error {
	if t.ID == "" {
		return errors.New("task requires an id")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (`+taskColumns+`)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Title, t.Description, t.Slug, t.ProjectRoot, t.ProjectName, t.Role, t.Runtime,
		string(t.Isolation), string(t.Status), t.Worktree, t.Branch, t.BaseRef,
		t.HerdrAgent, t.HerdrWorkspaceID, t.HerdrTabID, t.HerdrPaneID, t.HerdrSession,
		t.ParentTaskID, t.Error,
		timeString(t.CreatedAt), timeString(t.UpdatedAt), nullableTime(t.CompletedAt))
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// Update writes a task back, refreshing updated_at.
func (s *Store) Update(ctx context.Context, t *Task) error {
	t.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET
        title=?, description=?, slug=?, project_root=?, project_name=?, role=?, runtime=?,
        isolation=?, status=?, worktree=?, branch=?, base_ref=?, herdr_agent=?,
        herdr_workspace_id=?, herdr_tab_id=?, herdr_pane_id=?, herdr_session=?,
        parent_task_id=?, error=?, updated_at=?, completed_at=?
        WHERE id=?`,
		t.Title, t.Description, t.Slug, t.ProjectRoot, t.ProjectName, t.Role, t.Runtime,
		string(t.Isolation), string(t.Status), t.Worktree, t.Branch, t.BaseRef, t.HerdrAgent,
		t.HerdrWorkspaceID, t.HerdrTabID, t.HerdrPaneID, t.HerdrSession,
		t.ParentTaskID, t.Error, timeString(t.UpdatedAt), nullableTime(t.CompletedAt), t.ID)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus updates only the lifecycle status (and completed_at when final).
func (s *Store) SetStatus(ctx context.Context, id string, status Status) error {
	var completed any
	if status == StatusCompleted || status == StatusStopped {
		completed = nowString()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status=?, updated_at=?, completed_at=COALESCE(?, completed_at) WHERE id=?`,
		string(status), nowString(), completed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a task row.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get fetches one task by exact id.
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// Filter narrows a task listing.
type Filter struct {
	// Statuses restricts to these dispatch statuses.
	Statuses []Status
	// ProjectRoot restricts to one project.
	ProjectRoot string
	// ParentTaskID restricts to the children of one task.
	ParentTaskID string
	// IncludeTerminal includes stopped/completed/failed tasks.
	IncludeTerminal bool
	// Limit caps the result count; 0 means no limit.
	Limit int
}

// ActiveFilter lists tasks dispatch still considers live.
func ActiveFilter() Filter {
	return Filter{Statuses: []Status{StatusPending, StatusRunning}}
}

// List returns tasks matching a filter, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Task, error) {
	query := `SELECT ` + taskColumns + ` FROM tasks`
	var where []string
	var args []any

	if len(f.Statuses) > 0 {
		placeholders := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	} else if !f.IncludeTerminal {
		where = append(where, "status IN ('pending','running')")
	}
	if f.ProjectRoot != "" {
		where = append(where, "project_root = ?")
		args = append(args, f.ProjectRoot)
	}
	if f.ParentTaskID != "" {
		where = append(where, "parent_task_id = ?")
		args = append(args, f.ParentTaskID)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// All returns every task, newest first.
func (s *Store) All(ctx context.Context) ([]Task, error) {
	return s.List(ctx, Filter{IncludeTerminal: true})
}

// Children returns the tasks dispatched underneath a parent.
func (s *Store) Children(ctx context.Context, parentID string) ([]Task, error) {
	return s.List(ctx, Filter{ParentTaskID: parentID, IncludeTerminal: true})
}

// Resolve finds exactly one task from a human reference: a full id, an id
// suffix, a herdr agent name, or a task slug (or unique slug prefix).
//
// Users should never have to memorise a UUID, and dispatch should never guess
// between two candidates.
func (s *Store) Resolve(ctx context.Context, ref string) (Task, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Task{}, ErrNotFound
	}

	// An exact id, agent name or slug wins outright.
	for _, query := range []string{
		`SELECT ` + taskColumns + ` FROM tasks WHERE id = ?`,
		`SELECT ` + taskColumns + ` FROM tasks WHERE herdr_agent = ? ORDER BY created_at DESC`,
		`SELECT ` + taskColumns + ` FROM tasks WHERE slug = ? ORDER BY created_at DESC`,
	} {
		matches, err := s.queryTasks(ctx, query, ref)
		if err != nil {
			return Task{}, err
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			if live := preferLive(matches); live != nil {
				return *live, nil
			}
			return Task{}, &AmbiguousError{Ref: ref, Matches: matches}
		}
	}

	// Then prefix matches on id, slug and agent name.
	matches, err := s.queryTasks(ctx,
		`SELECT `+taskColumns+` FROM tasks
         WHERE id LIKE ? OR id LIKE ? OR slug LIKE ? OR herdr_agent LIKE ?
         ORDER BY created_at DESC`,
		ref+"%", "%"+ref, ref+"%", ref+"%")
	if err != nil {
		return Task{}, err
	}
	switch {
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		if live := preferLive(matches); live != nil {
			return *live, nil
		}
		return Task{}, &AmbiguousError{Ref: ref, Matches: matches}
	}
	return Task{}, ErrNotFound
}

// preferLive disambiguates by liveness: if exactly one candidate is still
// active, that is unambiguously the one the user means.
func preferLive(matches []Task) *Task {
	var live *Task
	count := 0
	for i := range matches {
		if !matches[i].Status.Terminal() {
			count++
			live = &matches[i]
		}
	}
	if count == 1 {
		return live
	}
	return nil
}

func (s *Store) queryTasks(ctx context.Context, query string, args ...any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Counts summarises the registry for the TUI home screen.
type Counts struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Stopped int `json:"stopped"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// Count returns task counts by status.
func (s *Store) Count(ctx context.Context) (Counts, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(1) FROM tasks GROUP BY status`)
	if err != nil {
		return Counts{}, err
	}
	defer rows.Close()
	var c Counts
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Counts{}, err
		}
		c.Total += n
		switch Status(status) {
		case StatusPending, StatusRunning:
			c.Active += n
		case StatusStopped:
			c.Stopped += n
		case StatusCompleted:
			c.Done += n
		case StatusFailed:
			c.Failed += n
		}
	}
	return c, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(row scanner) (Task, error) {
	var (
		t           Task
		isolation   string
		status      string
		createdAt   string
		updatedAt   string
		completedAt sql.NullString
	)
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Slug, &t.ProjectRoot, &t.ProjectName,
		&t.Role, &t.Runtime, &isolation, &status, &t.Worktree, &t.Branch, &t.BaseRef,
		&t.HerdrAgent, &t.HerdrWorkspaceID, &t.HerdrTabID, &t.HerdrPaneID, &t.HerdrSession,
		&t.ParentTaskID, &t.Error, &createdAt, &updatedAt, &completedAt)
	if err != nil {
		return Task{}, err
	}
	t.Isolation = roles.IsolationMode(isolation)
	t.Status = Status(status)
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	if completedAt.Valid && completedAt.String != "" {
		ts := parseTime(completedAt.String)
		t.CompletedAt = &ts
	}
	return t, nil
}

func timeString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func nowString() string             { return timeString(time.Now()) }

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeString(*t)
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}
