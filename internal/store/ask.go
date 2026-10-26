package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AskKind distinguishes a decision (pick an option) from a chore (a checklist
// only the human can do).
type AskKind string

const (
	AskDecision AskKind = "decision"
	AskChore    AskKind = "chore"
)

// AskState is where an ask is in its life. Answered means the answer has not
// yet been delivered to the agent.
type AskState string

const (
	AskOpen      AskState = "open"
	AskAnswered  AskState = "answered"
	AskDelivered AskState = "delivered"
	AskWithdrawn AskState = "withdrawn"
)

// AskOption is one choice offered by a decision.
type AskOption struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// Ask is something an agent needs from the human.
type Ask struct {
	ID          string      `json:"id"`
	TaskID      string      `json:"task_id"`
	Kind        AskKind     `json:"kind"`
	Title       string      `json:"title"`
	Body        string      `json:"body,omitempty"`
	Options     []AskOption `json:"options,omitempty"`
	Recommended int         `json:"recommended"` // 0-based index into Options, -1 for chores
	Items       []string    `json:"items,omitempty"`
	Blocks      string      `json:"blocks,omitempty"`
	State       AskState    `json:"state"`
	Answer      string      `json:"answer,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	AnsweredAt  *time.Time  `json:"answered_at,omitempty"`
	DeliveredAt *time.Time  `json:"delivered_at,omitempty"`
}

// Report is a task's latest self-reported status.
type Report struct {
	TaskID    string    `json:"task_id"`
	Line      string    `json:"line"`
	Done      int       `json:"done"`
	Total     int       `json:"total"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AskFilter narrows an ask listing; empty fields match everything.
type AskFilter struct {
	TaskIDs []string
	States  []AskState
}

const askColumns = `id, task_id, kind, title, body, options_json, recommended, items_json,
    blocks, state, answer, created_at, answered_at, delivered_at`

// CreateAsk inserts a new ask, defaulting its state to open.
func (s *Store) CreateAsk(ctx context.Context, a *Ask) error {
	if a.ID == "" {
		return errors.New("ask requires an id")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.State == "" {
		a.State = AskOpen
	}
	options, err := json.Marshal(a.Options)
	if err != nil {
		return fmt.Errorf("encode ask options: %w", err)
	}
	items, err := json.Marshal(a.Items)
	if err != nil {
		return fmt.Errorf("encode ask items: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO asks (`+askColumns+`)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, string(a.Kind), a.Title, a.Body, string(options), a.Recommended, string(items),
		a.Blocks, string(a.State), a.Answer, timeString(a.CreatedAt),
		nullableTime(a.AnsweredAt), nullableTime(a.DeliveredAt))
	if err != nil {
		return fmt.Errorf("insert ask: %w", err)
	}
	return nil
}

// GetAsk fetches one ask by exact id.
func (s *Store) GetAsk(ctx context.Context, id string) (Ask, error) {
	a, err := scanAsk(s.db.QueryRowContext(ctx, `SELECT `+askColumns+` FROM asks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Ask{}, ErrNotFound
	}
	return a, err
}

// ListAsks returns asks matching a filter, newest first.
func (s *Store) ListAsks(ctx context.Context, f AskFilter) ([]Ask, error) {
	query := `SELECT ` + askColumns + ` FROM asks`
	var where []string
	var args []any
	if len(f.TaskIDs) > 0 {
		where = append(where, "task_id IN ("+placeholders(len(f.TaskIDs))+")")
		for _, id := range f.TaskIDs {
			args = append(args, id)
		}
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ask
	for rows.Next() {
		a, err := scanAsk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAskState moves an ask to a new state. Answered stamps answered_at and
// records the answer; delivered stamps delivered_at and keeps the answer.
func (s *Store) SetAskState(ctx context.Context, id string, state AskState, answer string) error {
	var res sql.Result
	var err error
	switch state {
	case AskAnswered:
		res, err = s.db.ExecContext(ctx, `UPDATE asks SET state=?, answer=?, answered_at=? WHERE id=?`,
			string(state), answer, nowString(), id)
	case AskDelivered:
		res, err = s.db.ExecContext(ctx, `UPDATE asks SET state=?, delivered_at=? WHERE id=?`,
			string(state), nowString(), id)
	default:
		res, err = s.db.ExecContext(ctx, `UPDATE asks SET state=? WHERE id=?`, string(state), id)
	}
	if err != nil {
		return fmt.Errorf("update ask: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetReport records a task's status, replacing any earlier report.
func (s *Store) SetReport(ctx context.Context, r Report) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_reports (task_id, line, done, total, updated_at)
        VALUES (?,?,?,?,?)
        ON CONFLICT(task_id) DO UPDATE SET line=excluded.line, done=excluded.done,
            total=excluded.total, updated_at=excluded.updated_at`,
		r.TaskID, r.Line, r.Done, r.Total, nowString())
	if err != nil {
		return fmt.Errorf("upsert report: %w", err)
	}
	return nil
}

// Reports returns the latest report for each of the given tasks that has one.
func (s *Store) Reports(ctx context.Context, taskIDs []string) (map[string]Report, error) {
	out := map[string]Report{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, line, done, total, updated_at
        FROM task_reports WHERE task_id IN (`+placeholders(len(taskIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Report
		var updatedAt string
		if err := rows.Scan(&r.TaskID, &r.Line, &r.Done, &r.Total, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = parseTime(updatedAt)
		out[r.TaskID] = r
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func scanAsk(row scanner) (Ask, error) {
	var (
		a                                 Ask
		kind, state                       string
		optionsJSON, itemsJSON, createdAt string
		answeredAt, deliveredAt           sql.NullString
	)
	err := row.Scan(&a.ID, &a.TaskID, &kind, &a.Title, &a.Body, &optionsJSON, &a.Recommended,
		&itemsJSON, &a.Blocks, &state, &a.Answer, &createdAt, &answeredAt, &deliveredAt)
	if err != nil {
		return Ask{}, err
	}
	if err := json.Unmarshal([]byte(optionsJSON), &a.Options); err != nil {
		return Ask{}, fmt.Errorf("decode ask options: %w", err)
	}
	if err := json.Unmarshal([]byte(itemsJSON), &a.Items); err != nil {
		return Ask{}, fmt.Errorf("decode ask items: %w", err)
	}
	a.Kind = AskKind(kind)
	a.State = AskState(state)
	a.CreatedAt = parseTime(createdAt)
	if answeredAt.Valid && answeredAt.String != "" {
		ts := parseTime(answeredAt.String)
		a.AnsweredAt = &ts
	}
	if deliveredAt.Valid && deliveredAt.String != "" {
		ts := parseTime(deliveredAt.String)
		a.DeliveredAt = &ts
	}
	return a, nil
}
