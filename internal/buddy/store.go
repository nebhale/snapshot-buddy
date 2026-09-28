package buddy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("operation conflicts with active work")

type Store struct {
	changes changes
	db      *sql.DB
	lock    *os.File
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6], b[8] = (b[6]&15)|64, (b[8]&63)|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".snapshot-buddy.lock"), os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("data directory is already in use by another Snapshot Buddy process")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "snapshot-buddy.db"))
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	if err := s.initialize(); err != nil {
		db.Close()
		lock.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { err := s.db.Close(); s.lock.Close(); return err }

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 3 {
		return fmt.Errorf("database schema %d is newer than this application supports", version)
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY, printer_id TEXT NOT NULL, name TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','closed')), close_reason TEXT NOT NULL DEFAULT '',
 opened_at TEXT NOT NULL, closed_at TEXT, recovered INTEGER NOT NULL, opened_by TEXT NOT NULL,
 first_layer INTEGER, source_ip TEXT NOT NULL, initial_marker TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS active_printer ON sessions(printer_id) WHERE state='active';
CREATE INDEX IF NOT EXISTS session_history ON sessions(opened_at DESC);
CREATE TABLE IF NOT EXISTS captures (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, layer INTEGER NOT NULL, received_at TEXT NOT NULL, captured_at TEXT,
 source_ip TEXT NOT NULL, raw TEXT NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
 file TEXT NOT NULL DEFAULT '', width INTEGER NOT NULL DEFAULT 0, height INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0,
 UNIQUE(session_id, kind, layer)
);
CREATE TABLE IF NOT EXISTS recent_events (
 printer_id TEXT NOT NULL, kind TEXT NOT NULL, raw TEXT NOT NULL, received_at TEXT NOT NULL,
 PRIMARY KEY(printer_id, kind)
);
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, duration_ms INTEGER NOT NULL, frame_count INTEGER NOT NULL,
 state TEXT NOT NULL, progress REAL NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, manifest TEXT NOT NULL,
 UNIQUE(session_id, revision, duration_ms)
);
UPDATE captures SET kind='LAYER' WHERE kind='FRAME';
UPDATE recent_events SET kind='LAYER' WHERE kind='FRAME';
UPDATE sessions SET opened_by='layer' WHERE opened_by='frame';
UPDATE sessions SET revision=revision+1 WHERE id IN (SELECT session_id FROM captures WHERE status='pending');
UPDATE captures SET status='failed', error='Application stopped before capture was committed; no later frame substituted' WHERE status='pending';
UPDATE jobs SET state='queued', progress=0, error='' WHERE state='running';
`)
	if err != nil {
		return err
	}
	if version < 3 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN display_name TEXT NOT NULL DEFAULT ''; PRAGMA user_version=3;`); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

// Fixed precision keeps SQLite's text timestamp ordering chronological.
func stamp(t time.Time) string     { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }

const sessionSelect = `SELECT s.id,s.printer_id,s.name,s.display_name,s.state,s.close_reason,s.opened_at,s.closed_at,s.recovered,s.opened_by,s.first_layer,s.source_ip,s.initial_marker,s.revision,
 (SELECT count(*) FROM captures WHERE session_id=s.id AND status='saved'),
 (SELECT count(*) FROM captures WHERE session_id=s.id AND status='failed'),
 (SELECT count(*) FROM captures WHERE session_id=s.id AND status='pending') FROM sessions s `

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (Session, error) {
	var v Session
	var opened string
	var closed sql.NullString
	var layer sql.NullInt64
	err := row.Scan(&v.ID, &v.PrinterID, &v.Name, &v.DisplayName, &v.State, &v.CloseReason, &opened, &closed, &v.Recovered, &v.OpenedBy, &layer, &v.SourceIP, &v.InitialMarker, &v.Revision, &v.FrameCount, &v.FailureCount, &v.PendingCount)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.OpenedAt = parseTime(opened)
	if closed.Valid {
		t := parseTime(closed.String)
		v.ClosedAt = &t
	}
	if layer.Valid {
		n := int(layer.Int64)
		v.FirstLayer = &n
	}
	return v, err
}

func (s *Store) Session(id string) (Session, error) {
	return scanSession(s.db.QueryRow(sessionSelect+`WHERE s.id=?`, id))
}
func (s *Store) Active(printer string) (Session, error) {
	return scanSession(s.db.QueryRow(sessionSelect+`WHERE s.printer_id=? AND s.state='active'`, printer))
}

func (s *Store) Sessions(printer string, limit, offset int) ([]Session, error) {
	rows, err := s.db.Query(sessionSelect+`WHERE (?='' OR s.printer_id=?) ORDER BY s.opened_at DESC,s.id DESC LIMIT ? OFFSET ?`, printer, printer, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// Apply records the event and reserves a capture before any camera request.
// A transaction makes duplicate markers harmless, even across restarts.
func (s *Store) Apply(ctx context.Context, e Event) (*Capture, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var raw, received string
	err = tx.QueryRow(`SELECT raw,received_at FROM recent_events WHERE printer_id=? AND kind=?`, e.PrinterID, e.Kind).Scan(&raw, &received)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	duplicateWindow := 2 * time.Second
	if e.Kind == "START" || e.Kind == "STOP" {
		// Lifecycle retries span 2.2 seconds. Keep the whole burst harmless,
		// with room for delivery jitter, without changing layer handling.
		duplicateWindow = 5 * time.Second
	}
	if err == nil && raw == e.Raw && e.ReceivedAt.Sub(parseTime(received)) >= 0 && e.ReceivedAt.Sub(parseTime(received)) < duplicateWindow {
		return nil, "duplicate", nil
	}
	var sessionID string
	err = tx.QueryRow(`SELECT id FROM sessions WHERE printer_id=? AND state='active'`, e.PrinterID).Scan(&sessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	if e.Kind == "STOP" && sessionID == "" {
		return nil, "orphan_stop", nil
	}
	supersededID := ""
	if e.Kind == "START" && sessionID != "" {
		supersededID = sessionID
		if _, err = tx.Exec(`UPDATE sessions SET state='closed',close_reason='superseded',closed_at=?,revision=revision+1 WHERE id=?`, stamp(e.ReceivedAt), sessionID); err != nil {
			return nil, "", err
		}
		sessionID = ""
	}
	if sessionID == "" {
		sessionID = newID()
		name, openedBy := e.Name, "start"
		var first any
		recovered := e.Kind == "LAYER"
		if recovered {
			name = "Recovered print — " + e.ReceivedAt.UTC().Format("2006-01-02 15:04:05 UTC")
			openedBy = "layer"
			first = e.Layer
		}
		_, err = tx.Exec(`INSERT INTO sessions(id,printer_id,name,state,opened_at,recovered,opened_by,first_layer,source_ip,initial_marker) VALUES(?,?,?,'active',?,?,?,?,?,?)`, sessionID, e.PrinterID, name, stamp(e.ReceivedAt), recovered, openedBy, first, e.SourceIP, e.Raw)
		if err != nil {
			return nil, "", err
		}
	}
	_, err = tx.Exec(`INSERT INTO recent_events VALUES(?,?,?,?) ON CONFLICT(printer_id,kind) DO UPDATE SET raw=excluded.raw,received_at=excluded.received_at`, e.PrinterID, e.Kind, e.Raw, stamp(e.ReceivedAt))
	if err != nil {
		return nil, "", err
	}
	var capture *Capture
	if e.Kind != "START" {
		res, err := tx.Exec(`INSERT INTO captures(session_id,kind,layer,received_at,source_ip,raw,status) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT(session_id,kind,layer) DO NOTHING`, sessionID, e.Kind, e.Layer, stamp(e.ReceivedAt), e.SourceIP, e.Raw)
		if err != nil {
			return nil, "", err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			id, _ := res.LastInsertId()
			capture = &Capture{ID: id, SessionID: sessionID, Kind: e.Kind, Layer: e.Layer, ReceivedAt: e.ReceivedAt, SourceIP: e.SourceIP, Raw: e.Raw, Status: "pending"}
			if _, err := tx.Exec(`UPDATE sessions SET revision=revision+1 WHERE id=?`, sessionID); err != nil {
				return nil, "", err
			}
		}
	}
	if e.Kind == "STOP" {
		if _, err := tx.Exec(`UPDATE sessions SET state='closed',close_reason='completed',closed_at=?,revision=revision+1 WHERE id=?`, stamp(e.ReceivedAt), sessionID); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	s.changes.publish()
	if supersededID != "" {
		slog.Info("session closed", "printer", e.PrinterID, "session", supersededID, "reason", "superseded")
	}
	return capture, sessionID, nil
}

func (s *Store) FinishCapture(c Capture) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var captured any
	if c.CapturedAt != nil {
		captured = stamp(*c.CapturedAt)
	}
	if _, err := tx.Exec(`UPDATE captures SET status=?,error=?,captured_at=?,file=?,width=?,height=?,size=? WHERE id=?`, c.Status, c.Error, captured, c.File, c.Width, c.Height, c.Size, c.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET revision=revision+1 WHERE id=?`, c.SessionID); err != nil {
		return err
	}
	return s.changed(tx.Commit())
}

func (s *Store) CloseSession(id string) error {
	res, err := s.db.Exec(`UPDATE sessions SET state='closed',close_reason='manual',closed_at=?,revision=revision+1 WHERE id=? AND state='active'`, stamp(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	return s.changed(nil)
}

const captureSelect = `SELECT id,session_id,kind,layer,received_at,captured_at,source_ip,raw,status,error,file,width,height,size FROM captures `

func scanCapture(row scanner) (Capture, error) {
	var v Capture
	var at string
	var captured sql.NullString
	err := row.Scan(&v.ID, &v.SessionID, &v.Kind, &v.Layer, &at, &captured, &v.SourceIP, &v.Raw, &v.Status, &v.Error, &v.File, &v.Width, &v.Height, &v.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.ReceivedAt = parseTime(at)
	if captured.Valid {
		t := parseTime(captured.String)
		v.CapturedAt = &t
	}
	return v, err
}

func (s *Store) Capture(id int64) (Capture, error) {
	return scanCapture(s.db.QueryRow(captureSelect+`WHERE id=?`, id))
}

func (s *Store) Snapshot(id string) (Manifest, error) { return s.snapshotSince(id, 0, nil) }

// Saved and failed captures are immutable. A live view only needs new IDs and
// the captures which were still pending at its last successful update.
func (s *Store) snapshotSince(id string, after int64, pending []int64) (Manifest, error) {
	m := Manifest{Version: 1, ExportedAt: time.Now().UTC(), Captures: []Capture{}}
	tx, err := s.db.Begin()
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	m.Session, err = scanSession(tx.QueryRow(sessionSelect+`WHERE s.id=?`, id))
	if err != nil {
		return m, err
	}
	query := captureSelect + `WHERE session_id=? AND (id>?`
	args := []any{id, after}
	for _, pendingID := range pending {
		query += ` OR id=?`
		args = append(args, pendingID)
	}
	query += `) ORDER BY id`
	rows, err := tx.Query(query, args...)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			rows.Close()
			return m, err
		}
		m.Captures = append(m.Captures, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	return m, tx.Commit()
}

const jobSelect = `SELECT id,session_id,revision,duration_ms,frame_count,state,progress,error,created_at,manifest FROM jobs `

func scanJob(row scanner) (Job, error) {
	var j Job
	var created, manifest string
	err := row.Scan(&j.ID, &j.SessionID, &j.Revision, &j.DurationMS, &j.FrameCount, &j.State, &j.Progress, &j.Error, &created, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.CreatedAt = parseTime(created)
	return j, json.Unmarshal([]byte(manifest), &j.Manifest)
}

func (s *Store) CreateJob(m Manifest, durationMS int64) (Job, error) {
	if len(m.Frames()) == 0 {
		return Job{}, errors.New("capture at least one frame before creating a video")
	}
	if durationMS < 1000 || durationMS > 3600000 {
		return Job{}, errors.New("duration must be between 1 and 3600 seconds")
	}
	// Keep every source frame within H.264's practical frame-rate range.
	if float64(len(m.Frames()))*1000/float64(durationMS) > 240 {
		return Job{}, errors.New("choose a longer duration: the resulting frame rate exceeds 240 fps")
	}
	data, err := json.Marshal(m)
	if err != nil {
		return Job{}, err
	}
	_, err = s.db.Exec(`INSERT INTO jobs(id,session_id,revision,duration_ms,frame_count,state,created_at,manifest) VALUES(?,?,?,?,?,'queued',?,?) ON CONFLICT(session_id,revision,duration_ms) DO UPDATE SET state=CASE WHEN jobs.state='failed' THEN 'queued' ELSE jobs.state END,error=CASE WHEN jobs.state='failed' THEN '' ELSE jobs.error END,progress=CASE WHEN jobs.state='failed' THEN 0 ELSE jobs.progress END`, newID(), m.Session.ID, m.Session.Revision, durationMS, len(m.Frames()), stamp(time.Now()), string(data))
	if err != nil {
		return Job{}, err
	}
	s.changes.publish()
	return scanJob(s.db.QueryRow(jobSelect+`WHERE session_id=? AND revision=? AND duration_ms=?`, m.Session.ID, m.Session.Revision, durationMS))
}

func (s *Store) Job(id string) (Job, error) {
	return scanJob(s.db.QueryRow(jobSelect+`WHERE id=?`, id))
}
func (s *Store) Jobs(session string) ([]Job, error) {
	rows, err := s.db.Query(jobSelect+`WHERE session_id=? ORDER BY created_at DESC`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) ClaimJob() (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRow(jobSelect + `WHERE state='queued' ORDER BY created_at LIMIT 1`))
	if err != nil {
		return j, err
	}
	if _, err = tx.Exec(`UPDATE jobs SET state='running',progress=0,error='' WHERE id=?`, j.ID); err != nil {
		return j, err
	}
	j.State = "running"
	return j, s.changed(tx.Commit())
}

func (s *Store) JobProgress(id string, progress float64) error {
	_, err := s.db.Exec(`UPDATE jobs SET progress=? WHERE id=?`, progress, id)
	return s.changed(err)
}
func (s *Store) FinishJob(id, state, message string) error {
	_, err := s.db.Exec(`UPDATE jobs SET state=?,error=?,progress=CASE WHEN ?='ready' THEN 1 ELSE progress END WHERE id=?`, state, message, state, id)
	return s.changed(err)
}

func (s *Store) CanDelete(id string) error {
	ss, err := s.Session(id)
	if err != nil {
		return err
	}
	if ss.State == "active" || ss.PendingCount > 0 {
		return ErrConflict
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM jobs WHERE session_id=? AND state IN ('queued','running')`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return s.changed(err)
}
