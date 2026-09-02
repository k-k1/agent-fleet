package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// sqlDB wraps *sql.DB to rewrite placeholders per dialect before delegating, so
// the store's dialect-neutral `?` queries run unchanged on SQLite (positional ?)
// and Postgres ($1..$n). Every other *sql.DB method passes through the embedding.
type sqlDB struct {
	*sql.DB
	rb func(string) string
}

func (d *sqlDB) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.rb(q), a...)
}
func (d *sqlDB) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.rb(q), a...)
}
func (d *sqlDB) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.rb(q), a...)
}
func (d *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sqlTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlTx{Tx: tx, rb: d.rb}, nil
}

// sqlTx is the transaction analogue of sqlDB (same rebind).
type sqlTx struct {
	*sql.Tx
	rb func(string) string
}

func (t *sqlTx) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.rb(q), a...)
}
func (t *sqlTx) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.rb(q), a...)
}
func (t *sqlTx) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.rb(q), a...)
}

// rebindNoop leaves ? placeholders as-is (SQLite / modernc uses positional ?).
func rebindNoop(q string) string { return q }

// rebindDollar rewrites ordinal ? placeholders to Postgres $1..$n, skipping any
// inside single-quoted string literals (the store has none today; defensive).
func rebindDollar(q string) string {
	var b strings.Builder
	n := 0
	inStr := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inStr = !inStr
			b.WriteByte(c)
		case c == '?' && !inStr:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
