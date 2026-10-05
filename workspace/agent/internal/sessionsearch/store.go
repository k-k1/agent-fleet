package sessionsearch

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion is bumped whenever the tables or IndexText's output change. The index is a cache
// of transcripts that stay where they are, so a mismatch drops it and the next pass rebuilds it;
// there is no migration to write.
const schemaVersion = "2"

// Store is the on-disk index: one SQLite file holding a plain table of indexed turns and a
// contentless FTS5 table over their bigram-rewritten text. Contentless because the original text
// is already in the turns table, and storing the rewritten copy as well would roughly triple
// the file for nothing a reader ever sees.
type Store struct {
	db *sql.DB
}

// Open opens or creates the index at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// One connection: the only writer is the indexing pass, which holds it for one session at a
	// time, so a search waits at most that long and never meets SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		return err
	}
	var v string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k='schema'`).Scan(&v)
	if err == nil && v == schemaVersion {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stmts := []string{
		`DROP TABLE IF EXISTS fts`,
		`DROP TABLE IF EXISTS turns`,
		`DROP TABLE IF EXISTS sessions`,
		// docs = rows held; prefix/full = digests of every row but the last, and of every row,
		// which is how a pass tells "the transcript grew" from "the transcript is a different one".
		// settled = the session's StoppedAt when it was last indexed while stopped ("" otherwise).
		`CREATE TABLE sessions(name TEXT PRIMARY KEY, kind TEXT NOT NULL, docs INTEGER NOT NULL,
			prefix TEXT NOT NULL, full TEXT NOT NULL, settled TEXT NOT NULL)`,
		`CREATE TABLE turns(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, ord INTEGER NOT NULL,
			idx INTEGER NOT NULL, role TEXT NOT NULL, ts TEXT NOT NULL, text TEXT NOT NULL)`,
		`CREATE INDEX turns_session_ord ON turns(session, ord)`,
		`CREATE VIRTUAL TABLE fts USING fts5(body, content='', contentless_delete=1,
			tokenize='unicode61 remove_diacritics 2')`,
		`INSERT OR REPLACE INTO meta(k, v) VALUES('schema', '` + schemaVersion + `')`,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("session search schema: %w", err)
		}
	}
	return tx.Commit()
}

// sessionState is what the index remembers about one session between passes.
type sessionState struct {
	Kind    string
	Docs    int
	Prefix  string
	Full    string
	Settled string
	Found   bool
}

func (s *Store) state(name string) (sessionState, error) {
	var st sessionState
	err := s.db.QueryRow(`SELECT kind, docs, prefix, full, settled FROM sessions WHERE name=?`, name).
		Scan(&st.Kind, &st.Docs, &st.Prefix, &st.Full, &st.Settled)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionState{}, nil
	}
	st.Found = err == nil
	return st, err
}

// digest covers every doc in docs, in order. It is the whole run rather than its last rows: two
// transcripts that end alike but differ earlier — an edited turn, a different conversation of the
// same length — must not read as the same one, or the earlier text stays searchable.
func digest(docs []Doc) string {
	h := sha256.New()
	for _, d := range docs {
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00%d:%s\x00", d.Idx, d.Role, d.TS, len(d.Text), d.Text)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func prefixAndFull(docs []Doc) (prefix, full string) {
	if n := len(docs); n > 0 {
		return digest(docs[:n-1]), digest(docs)
	}
	return digest(nil), digest(nil)
}

// Apply brings one session's rows in line with docs, the session's whole conversation as just
// read. A transcript normally only grows, so when every stored row but the last is still in
// place, unchanged, only the last row (an assistant turn may still have been streaming when it
// was indexed) and what follows it are rewritten. Anything else — a shorter transcript, a claude sid that
// moved to a sibling jsonl, an edited file — replaces the session's rows outright, which is
// always correct and only slower.
func (s *Store) Apply(name, kind string, docs []Doc, settled string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var st sessionState
	err = tx.QueryRow(`SELECT kind, docs, prefix, full, settled FROM sessions WHERE name=?`, name).
		Scan(&st.Kind, &st.Docs, &st.Prefix, &st.Full, &st.Settled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	prefix, full := prefixAndFull(docs)
	start := 0
	switch {
	case st.Kind != kind:
		// A different kind under the same name cannot be the same conversation.
	case st.Docs == len(docs) && st.Full == full:
		start = len(docs) // unchanged
	case st.Docs >= 1 && len(docs) >= st.Docs && digest(docs[:st.Docs-1]) == st.Prefix:
		start = st.Docs - 1
	}
	if start < len(docs) || start < st.Docs {
		if err := deleteFrom(tx, name, start); err != nil {
			return err
		}
		ins, err := tx.Prepare(`INSERT INTO turns(session, ord, idx, role, ts, text) VALUES(?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		fts, err := tx.Prepare(`INSERT INTO fts(rowid, body) VALUES(?, ?)`)
		if err != nil {
			return err
		}
		defer fts.Close()
		for i := start; i < len(docs); i++ {
			d := docs[i]
			res, err := ins.Exec(name, i, d.Idx, d.Role, d.TS, d.Text)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := fts.Exec(id, IndexText(d.Text)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO sessions(name, kind, docs, prefix, full, settled) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET kind=excluded.kind, docs=excluded.docs, prefix=excluded.prefix,
			full=excluded.full, settled=excluded.settled`,
		name, kind, len(docs), prefix, full, settled); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteFrom removes a session's rows from ordinal ord on, from both tables.
func deleteFrom(tx *sql.Tx, name string, ord int) error {
	if _, err := tx.Exec(`DELETE FROM fts WHERE rowid IN (SELECT id FROM turns WHERE session=? AND ord>=?)`, name, ord); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM turns WHERE session=? AND ord>=?`, name, ord)
	return err
}

// Forget removes every row of a session. Called when the session goes to the trash, so its text
// does not outlive it in a second place.
func (s *Store) Forget(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteFrom(tx, name, 0); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE name=?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// Names lists the sessions the index holds rows for, with their row counts.
func (s *Store) Names() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT name, docs FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// rawHit is one matching row with its final score.
type rawHit struct {
	Session string
	Doc
	Score float64 // score() of the row, computed in SQL
}

// matchOpts says which sessions to search and how to rank and cap the rows.
type matchOpts struct {
	Sessions   []string // the only sessions searched
	Scheduled  []string // the subset that weighs scheduleWeight
	Now        time.Time
	PerSession int
	Limit      int
}

// match runs an already-built MATCH expression and returns, best first by the final score, at
// most PerSession rows from any one session and Limit rows in all.
//
// The filter, the score and the per-session cap all run in SQL, before the limit. Filtering or
// capping a pool already cut to the top rows lets one session with hundreds of strong matches
// push every other session's only match out of it; capping by bm25 alone and adjusting after
// drops a recent row the adjustment would have ranked first. The SQL expression is score()'s,
// and TestMatchScoreIsScore holds the two together.
func (s *Store) match(expr string, o matchOpts) ([]rawHit, error) {
	names, err := json.Marshal(o.Sessions)
	if err != nil {
		return nil, err
	}
	sched, err := json.Marshal(append([]string{}, o.Scheduled...))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT session, idx, role, ts, text, sc FROM (
			SELECT session, idx, role, ts, text, sc,
				ROW_NUMBER() OVER (PARTITION BY session ORDER BY sc DESC) AS rn
			FROM (
				SELECT t.session, t.idx, t.role, t.ts, t.text,
					-m.r
					* CASE WHEN t.session IN (SELECT value FROM json_each(?)) THEN ? ELSE 1.0 END
					* COALESCE(pow(0.5, max(julianday(?) - julianday(t.ts), 0) / ?), 1.0) AS sc
				FROM (SELECT rowid AS id, bm25(fts) AS r FROM fts WHERE fts MATCH ?) m
				JOIN turns t ON t.id = m.id
				WHERE t.session IN (SELECT value FROM json_each(?))
			)
		) WHERE rn <= ? ORDER BY sc DESC LIMIT ?`,
		string(sched), scheduleWeight, o.Now.UTC().Format(time.RFC3339), recencyHalfLifeDays,
		expr, string(names), o.PerSession, o.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawHit
	for rows.Next() {
		var h rawHit
		if err := rows.Scan(&h.Session, &h.Idx, &h.Role, &h.TS, &h.Text, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// window returns the indexed turns around the one at transcript index idx: before turns ahead of
// it and after turns behind it. When no row has exactly idx, the nearest earlier one is the
// centre (the mirror's idx and the index's agree, but a re-read can drop a turn that was empty).
func (s *Store) window(name string, idx, before, after int) ([]Doc, error) {
	var ord int
	err := s.db.QueryRow(`SELECT ord FROM turns WHERE session=? AND idx<=? ORDER BY ord DESC LIMIT 1`, name, idx).Scan(&ord)
	if errors.Is(err, sql.ErrNoRows) {
		ord = 0
	} else if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT idx, role, ts, text FROM turns WHERE session=? AND ord BETWEEN ? AND ? ORDER BY ord`,
		name, ord-before, ord+after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Doc
	for rows.Next() {
		var d Doc
		if err := rows.Scan(&d.Idx, &d.Role, &d.TS, &d.Text); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
