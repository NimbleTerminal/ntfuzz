package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"nimbleterminal.dev/ntduckbox"
)

var ErrReadOnly = errors.New("snapshot store is read-only")

// OpenReadOnly opens an immutable snapshot through ntduckbox. It never migrates,
// creates files, loads extensions, spills to disk, or enables external access.
func OpenReadOnly(path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("an existing snapshot file is required")
	}
	db, err := ntduckbox.Open(ntduckbox.Options{Path: path, MaxRows: 10000, MaxChars: 16 << 20, QueryTimeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{readonly: db}
	version, err := s.SchemaVersion(context.Background())
	if err != nil || version != CurrentSchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("snapshot schema %d (need %d): %v", version, CurrentSchemaVersion, err)
	}
	return s, nil
}

type resultRows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}
type resultRow interface{ Scan(...any) error }

func (s *Store) query(ctx context.Context, query string, args ...any) (resultRows, error) {
	if s.readonly == nil {
		return s.db.QueryContext(ctx, query, args...)
	}
	bound, err := bindReadSQL(query, args)
	if err != nil {
		return nil, err
	}
	data, err := s.readonly.QueryFormat(ctx, bound, ntduckbox.JSONColumnar)
	if err != nil {
		return nil, err
	}
	// Preserve JSON object's column order. Map decoding would reorder columns.
	dec := json.NewDecoder(strings.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid columnar result")
	}
	r := &snapshotRows{position: -1}
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		var values []json.RawMessage
		if err := dec.Decode(&values); err != nil {
			return nil, err
		}
		if len(r.columns) > 0 && len(values) != len(r.columns[0]) {
			return nil, errors.New("inconsistent column lengths")
		}
		r.columns = append(r.columns, values)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) resultRow {
	if s.readonly == nil {
		return s.db.QueryRowContext(ctx, query, args...)
	}
	rows, err := s.query(ctx, query, args...)
	return snapshotRow{rows, err}
}

type snapshotRow struct {
	rows resultRows
	err  error
}

func (r snapshotRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer func() { _ = r.rows.Close() }()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return r.rows.Scan(dest...)
}

type snapshotRows struct {
	columns  [][]json.RawMessage
	position int
}

func (r *snapshotRows) Next() bool {
	r.position++
	return len(r.columns) > 0 && r.position < len(r.columns[0])
}
func (r *snapshotRows) Close() error { return nil }
func (r *snapshotRows) Err() error   { return nil }
func (r *snapshotRows) Scan(dest ...any) error {
	if len(dest) != len(r.columns) {
		return errors.New("snapshot scan column count mismatch")
	}
	if r.position < 0 || len(r.columns) == 0 || r.position >= len(r.columns[0]) {
		return io.EOF
	}
	for i, d := range dest {
		raw := r.columns[i][r.position]
		switch out := d.(type) {
		case *time.Time:
			t, err := snapshotTime(raw)
			if err != nil {
				return err
			}
			*out = t
		case *sql.NullTime:
			if string(raw) == "null" {
				*out = sql.NullTime{}
				continue
			}
			t, err := snapshotTime(raw)
			if err != nil {
				return err
			}
			*out = sql.NullTime{Time: t, Valid: true}
		case *sql.NullInt64:
			if string(raw) == "null" {
				*out = sql.NullInt64{}
				continue
			}
			var n int64
			if err := json.Unmarshal(raw, &n); err != nil {
				return err
			}
			*out = sql.NullInt64{Int64: n, Valid: true}
		default:
			if err := json.Unmarshal(raw, d); err != nil {
				return fmt.Errorf("snapshot column %d: %w", i, err)
			}
		}
	}
	return nil
}
func snapshotTime(raw json.RawMessage) (time.Time, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(layout, text); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid snapshot timestamp %q", text)
}

// Only application-owned SELECTs reach this adapter. Values are quoted as SQL
// literals, never interpolated as identifiers or syntax; ntduckbox validates the
// resulting single statement independently before preparing it.
func bindReadSQL(query string, args []any) (string, error) {
	var b strings.Builder
	quoted := false
	index := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '\'' {
			b.WriteByte(c)
			if quoted && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if c != '?' || quoted {
			b.WriteByte(c)
			continue
		}
		if index >= len(args) {
			return "", errors.New("missing query value")
		}
		switch v := args[index].(type) {
		case string:
			b.WriteString(sqlLiteral(v))
		case int:
			fmt.Fprint(&b, v)
		case int64:
			fmt.Fprint(&b, v)
		case time.Time:
			b.WriteString("TIMESTAMP " + sqlLiteral(v.UTC().Format("2006-01-02 15:04:05.999999999")))
		default:
			return "", fmt.Errorf("unsupported query value %T", v)
		}
		index++
	}
	if index != len(args) {
		return "", errors.New("unused query values")
	}
	return b.String(), nil
}
func sqlLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
