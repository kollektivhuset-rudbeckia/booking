// Package store persists bookings in SQLite. All times are stored as UTC
// RFC3339 strings so the database stays readable with the sqlite3 CLI.
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
)

// Status is the lifecycle state of a booking.
type Status string

const (
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
)

// Booking is one reservation of one resource.
type Booking struct {
	ID         string
	ResourceID string
	Start      time.Time
	End        time.Time
	Mode       string
	Name       string
	Apartment  string
	// MMUsername is the member's Mattermost username, lowercased. It is the
	// identity every per-member rule counts against.
	MMUsername string
	// MMUserID is the immutable Mattermost account id, used to reach the
	// member with a direct message.
	MMUserID string
	// Email comes from the Mattermost account and is kept for the admin view
	// and the calendar file. Nothing keys off it.
	Email string
	// Lang is the language this member reads, taken from their Mattermost
	// account when the booking was made. Messages about the booking go out in
	// it long after the browser that made it has gone.
	Lang        string
	Phone       string
	Note        string
	Status      Status
	CancelToken string
	CreatedAt   time.Time
	CancelledAt sql.NullTime
	CreatedIP   string
}

// Active reports whether the booking still occupies its slot.
func (b Booking) Active() bool { return b.Status == StatusConfirmed }

// Store is the persistence handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS bookings (
	id           TEXT PRIMARY KEY,
	resource_id  TEXT NOT NULL,
	start_utc    TEXT NOT NULL,
	end_utc      TEXT NOT NULL,
	mode         TEXT NOT NULL DEFAULT 'hours',
	name         TEXT NOT NULL,
	apartment    TEXT NOT NULL DEFAULT '',
	mm_username  TEXT NOT NULL DEFAULT '',
	mm_user_id   TEXT NOT NULL DEFAULT '',
	lang         TEXT NOT NULL DEFAULT '',
	email        TEXT NOT NULL DEFAULT '',
	phone        TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL DEFAULT 'confirmed',
	cancel_token TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	cancelled_at TEXT,
	created_ip   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_bookings_resource_time
	ON bookings (resource_id, start_utc, end_utc);
CREATE INDEX IF NOT EXISTS idx_bookings_status
	ON bookings (status, start_utc);
`

// Open opens (and if needed creates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	// _txlock=immediate makes every transaction take the write lock up front.
	// Without it two concurrent bookings can both start as readers and then
	// collide when they try to upgrade, which SQLite reports as
	// SQLITE_BUSY_SNAPSHOT and busy_timeout cannot retry away.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a small pool avoids lock churn.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate adds columns that arrived after the first release, and the indexes
// that depend on them. A database from before the Mattermost switch has
// bookings but no Mattermost columns; adding them empty keeps that history
// readable instead of throwing it away. This runs after the schema above, so
// the member index is created here rather than there: on an old database the
// column does not exist yet when the schema is applied.
func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(bookings)`)
	if err != nil {
		return fmt.Errorf("read table info: %w", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("read table info: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read table info: %w", err)
	}
	for _, col := range []string{"mm_username", "mm_user_id", "lang"} {
		if have[col] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE bookings ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add column %s: %w", col, err)
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_bookings_member ON bookings (mm_username, start_utc)`); err != nil {
		return fmt.Errorf("index members: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// ErrConflict is returned when a booking overlaps an existing one.
var ErrConflict = errors.New("the time is already booked")

// ErrNotFound is returned when a booking id or token does not exist.
var ErrNotFound = errors.New("no such booking")

const selectCols = `id, resource_id, start_utc, end_utc, mode, name, apartment,
	mm_username, mm_user_id, email, lang, phone, note, status, cancel_token,
	created_at, cancelled_at, created_ip`

func scan(rows interface{ Scan(...any) error }) (Booking, error) {
	var b Booking
	var start, end, created string
	var cancelled sql.NullString
	err := rows.Scan(&b.ID, &b.ResourceID, &start, &end, &b.Mode, &b.Name, &b.Apartment,
		&b.MMUsername, &b.MMUserID, &b.Email, &b.Lang, &b.Phone, &b.Note, &b.Status,
		&b.CancelToken, &created, &cancelled, &b.CreatedIP)
	if err != nil {
		return b, err
	}
	if b.Start, err = time.Parse(time.RFC3339, start); err != nil {
		return b, err
	}
	if b.End, err = time.Parse(time.RFC3339, end); err != nil {
		return b, err
	}
	if b.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return b, err
	}
	if cancelled.Valid && cancelled.String != "" {
		t, err := time.Parse(time.RFC3339, cancelled.String)
		if err != nil {
			return b, err
		}
		b.CancelledAt = sql.NullTime{Time: t, Valid: true}
	}
	return b, nil
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Create inserts a booking, refusing to write if it overlaps an existing one
// (including the required buffer, which the caller folds into blockFrom/blockTo).
// The check and the insert share a transaction so two simultaneous requests
// cannot both win.
func (s *Store) Create(ctx context.Context, b Booking, blockFrom, blockTo time.Time) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bookings
		WHERE resource_id = ? AND status = ?
		  AND start_utc < ? AND end_utc > ?`,
		b.ResourceID, StatusConfirmed, utc(blockTo), utc(blockFrom)).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO bookings (`+selectCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.ResourceID, utc(b.Start), utc(b.End), b.Mode, b.Name, b.Apartment,
		Member(b.MMUsername), b.MMUserID, b.Email, b.Lang, b.Phone, b.Note, b.Status,
		b.CancelToken, utc(b.CreatedAt), nil, b.CreatedIP)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns a single booking by id.
func (s *Store) Get(ctx context.Context, id string) (Booking, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM bookings WHERE id = ?`, id)
	b, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// Cancel marks a booking cancelled. The token must match unless force is set,
// which is how the admin view cancels on someone's behalf.
func (s *Store) Cancel(ctx context.Context, id, token string, force bool, at time.Time) error {
	q := `UPDATE bookings SET status = ?, cancelled_at = ? WHERE id = ? AND status = ?`
	args := []any{StatusCancelled, utc(at), id, StatusConfirmed}
	if !force {
		q += ` AND cancel_token = ?`
		args = append(args, token)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// InRange returns confirmed bookings for a resource that overlap [from, to).
// An empty resourceID means every resource.
func (s *Store) InRange(ctx context.Context, resourceID string, from, to time.Time) ([]Booking, error) {
	q := `SELECT ` + selectCols + ` FROM bookings
	      WHERE status = ? AND start_utc < ? AND end_utc > ?`
	args := []any{StatusConfirmed, utc(to), utc(from)}
	if resourceID != "" {
		q += ` AND resource_id = ?`
		args = append(args, resourceID)
	}
	q += ` ORDER BY start_utc`
	return s.query(ctx, q, args...)
}

// Filter describes an arbitrary booking search, used by the admin view.
type Filter struct {
	ResourceID string
	Member     string
	Query      string
	From       *time.Time
	To         *time.Time
	Status     Status
	Limit      int
	Ascending  bool
}

// Search returns bookings matching f.
func (s *Store) Search(ctx context.Context, f Filter) ([]Booking, error) {
	var where []string
	var args []any
	if f.ResourceID != "" {
		where = append(where, "resource_id = ?")
		args = append(args, f.ResourceID)
	}
	if f.Member != "" {
		where = append(where, "mm_username = ?")
		args = append(args, Member(f.Member))
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.From != nil {
		where = append(where, "end_utc > ?")
		args = append(args, utc(*f.From))
	}
	if f.To != nil {
		where = append(where, "start_utc < ?")
		args = append(args, utc(*f.To))
	}
	if f.Query != "" {
		where = append(where, `(lower(name) LIKE ? OR mm_username LIKE ? OR lower(email) LIKE ?
			OR lower(apartment) LIKE ? OR lower(note) LIKE ?)`)
		like := "%" + strings.ToLower(f.Query) + "%"
		args = append(args, like, like, like, like, like)
	}
	q := `SELECT ` + selectCols + ` FROM bookings`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	if f.Ascending {
		q += ` ORDER BY start_utc ASC`
	} else {
		q += ` ORDER BY start_utc DESC`
	}
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	return s.query(ctx, q, args...)
}

// Member normalizes a Mattermost username into the form used as the member
// key: lowercase, no leading @.
func Member(username string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
}

// ByMember returns a member's own bookings, earliest first.
func (s *Store) ByMember(ctx context.Context, username string, includeCancelled bool, since time.Time) ([]Booking, error) {
	if Member(username) == "" {
		return nil, nil
	}
	q := `SELECT ` + selectCols + ` FROM bookings WHERE mm_username = ? AND end_utc > ?`
	args := []any{Member(username), utc(since)}
	if !includeCancelled {
		q += ` AND status = ?`
		args = append(args, StatusConfirmed)
	}
	q += ` ORDER BY start_utc ASC`
	return s.query(ctx, q, args...)
}

// CountActiveForUser counts a member's confirmed bookings of one resource that
// have not yet ended.
func (s *Store) CountActiveForUser(ctx context.Context, resourceID, username string, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bookings
		WHERE resource_id = ? AND mm_username = ? AND status = ? AND end_utc > ?`,
		resourceID, Member(username), StatusConfirmed, utc(now)).Scan(&n)
	return n, err
}

// HoursForUserBetween sums a member's booked hours for one resource inside a window.
func (s *Store) HoursForUserBetween(ctx context.Context, resourceID, username string, from, to time.Time) (float64, error) {
	rows, err := s.InRangeForUser(ctx, resourceID, username, from, to)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, b := range rows {
		start, end := b.Start, b.End
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			total += end.Sub(start).Hours()
		}
	}
	return total, nil
}

// InRangeForUser returns one member's confirmed bookings overlapping a window.
func (s *Store) InRangeForUser(ctx context.Context, resourceID, username string, from, to time.Time) ([]Booking, error) {
	return s.query(ctx, `SELECT `+selectCols+` FROM bookings
		WHERE resource_id = ? AND mm_username = ? AND status = ?
		  AND start_utc < ? AND end_utc > ? ORDER BY start_utc`,
		resourceID, Member(username), StatusConfirmed, utc(to), utc(from))
}

// Stats summarises the database for the admin dashboard.
type Stats struct {
	Total     int
	Upcoming  int
	Cancelled int
	Next30    int
}

// Stats computes the dashboard counters.
func (s *Store) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'confirmed' AND end_utc > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'confirmed' AND start_utc > ? AND start_utc < ? THEN 1 ELSE 0 END), 0)
		FROM bookings`,
		utc(now), utc(now), utc(now.Add(30*24*time.Hour))).
		Scan(&st.Total, &st.Upcoming, &st.Cancelled, &st.Next30)
	return st, err
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Booking, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Booking
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
