// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package export writes everything this instance holds, in a shape a human
// and a machine can both read.
//
// It exists for two jobs that must never diverge: `limelit export`, so your
// data is yours and leaving is a command rather than a support ticket, and
// the payload `limelit upgrade` sends to Limelit Cloud. One serializer, so a
// field that survives the upgrade is the same field you can read on disk.
//
// Everything streams. An instance running daily across seven engines
// accumulates chats faster than people expect, and an export that builds the
// whole document in memory first is an export that fails on the install that
// needed it most.
package export

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/limelit-co/open/internal/store"
)

// SchemaVersion is the shape of the exported document.
//
// It is written into every export so a reader knows what it is holding, and
// so the Cloud importer can refuse a payload it does not understand rather
// than half-reading it.
const SchemaVersion = 1

// Options narrows an export.
type Options struct {
	// Since keeps only answers created on or after this date (YYYY-MM-DD),
	// and everything hanging off them. Empty means everything.
	Since string
	// Now is the timestamp stamped on the export. Injected so a test does not
	// depend on the clock.
	Now string
}

// tables is every table an export carries, in the order they are written.
//
// Order is deliberate: a reader building an object graph gets the things
// other rows point at before the rows that point at them.
var tables = []string{
	"property", "competitors", "prompts", "targets",
	"evaluations", "chats", "mentions", "citations", "fanout", "usage",
}

// WriteJSON streams the whole instance as one JSON document.
//
// The envelope is written by hand and each table's rows are encoded one at a
// time, so memory stays flat no matter how many answers are stored.
func WriteJSON(ctx context.Context, db *store.DB, w io.Writer, opts Options) error {
	bw := &errWriter{w: w}

	bw.printf(`{"schema_version":%d`, SchemaVersion)
	if opts.Now != "" {
		bw.printf(`,"exported_at":%s`, quote(opts.Now))
	}
	if opts.Since != "" {
		bw.printf(`,"since":%s`, quote(opts.Since))
	}

	for _, table := range tables {
		bw.printf(`,%s:`, quote(table))
		if table == "property" {
			// The property is a singleton, so it is an object rather than an
			// array. A one-element array here would make every consumer
			// write an index.
			p, err := db.Property(ctx)
			if err != nil {
				return err
			}
			if err := writeValue(bw, map[string]any{
				"name": p.Name, "domain": p.Domain, "aliases": nonNil(p.Aliases),
			}); err != nil {
				return err
			}
			continue
		}
		if err := streamTable(ctx, db, bw, table, opts); err != nil {
			return err
		}
	}

	bw.printf("}\n")
	return bw.err
}

// streamTable writes one table as a JSON array, a row at a time.
func streamTable(ctx context.Context, db *store.DB, bw *errWriter, table string, opts Options) error {
	query, args := tableQuery(table, opts)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("export %s: %w", table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	bw.printf("[")
	first := true
	for rows.Next() {
		record, err := scanRow(rows, cols)
		if err != nil {
			return fmt.Errorf("export %s: %w", table, err)
		}
		if !first {
			bw.printf(",")
		}
		first = false
		if err := writeValue(bw, record); err != nil {
			return err
		}
		if bw.err != nil {
			return bw.err
		}
	}
	bw.printf("]")
	return rows.Err()
}

// WriteCSV writes one file per table into dir.
//
// A directory rather than a zip, because the first thing anyone does with a
// CSV export is open one file, and asking them to unzip first is friction
// with no benefit at this size.
func WriteCSV(ctx context.Context, db *store.DB, dir string, opts Options) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string

	for _, table := range tables {
		if table == "property" {
			path := filepath.Join(dir, "property.csv")
			p, err := db.Property(ctx)
			if err != nil {
				return nil, err
			}
			if err := writeCSVFile(path,
				[]string{"name", "domain", "aliases"},
				[][]string{{p.Name, p.Domain, strings.Join(p.Aliases, "|")}}); err != nil {
				return nil, err
			}
			written = append(written, path)
			continue
		}

		query, args := tableQuery(table, opts)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("export %s: %w", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}

		path := filepath.Join(dir, table+".csv")
		f, err := os.Create(path)
		if err != nil {
			rows.Close()
			return nil, err
		}
		cw := csv.NewWriter(f)
		if err := cw.Write(cols); err != nil {
			f.Close()
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			record, err := scanRow(rows, cols)
			if err != nil {
				f.Close()
				rows.Close()
				return nil, err
			}
			line := make([]string, len(cols))
			for i, c := range cols {
				line[i] = csvCell(record[c])
			}
			if err := cw.Write(line); err != nil {
				f.Close()
				rows.Close()
				return nil, err
			}
		}
		rows.Close()
		cw.Flush()
		if err := cw.Error(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

// tableQuery is the read for one table, with the since filter applied where
// it means something.
//
// A filter on chats has to reach the rows that hang off them: exporting a
// mention whose chat was filtered out would produce a file that does not
// describe anything.
func tableQuery(table string, opts Options) (string, []any) {
	var (
		args  []any
		since = strings.TrimSpace(opts.Since)
	)
	add := func(v any) string {
		args = append(args, v)
		return "?"
	}

	switch table {
	case "competitors":
		return `SELECT id, name, domain, category, created_at FROM competitor ORDER BY id`, nil
	case "prompts":
		return `SELECT id, text, category, location_country, branded, tags, active, created_at
		        FROM prompt ORDER BY id`, nil
	case "targets":
		return `SELECT id, spec, engine, provider, model, online, access, enabled, created_at
		        FROM target ORDER BY id`, nil
	case "evaluations":
		q := `SELECT id, status, planned, completed, failed, started_at, finished_at FROM evaluation`
		if since != "" {
			q += ` WHERE date(started_at) >= ` + add(since)
		}
		return q + ` ORDER BY id`, args
	case "chats":
		q := `SELECT id, evaluation_id, prompt_id, target_id, status, text, model, error,
		             input_tokens, output_tokens, calls, created_at FROM chat`
		if since != "" {
			q += ` WHERE date(created_at) >= ` + add(since)
		}
		return q + ` ORDER BY id`, args
	case "mentions":
		q := `SELECT m.id, m.chat_id, m.competitor_id, m.brand_key, m.brand_name,
		             m.offset_start, m.offset_end, m.list_rank
		      FROM mention m JOIN chat c ON c.id = m.chat_id`
		if since != "" {
			q += ` WHERE date(c.created_at) >= ` + add(since)
		}
		return q + ` ORDER BY m.id`, args
	case "citations":
		q := `SELECT ct.id, ct.chat_id, ct.url, ct.host, ct.site, ct.title, ct.position, ct.source_type
		      FROM citation ct JOIN chat c ON c.id = ct.chat_id`
		if since != "" {
			q += ` WHERE date(c.created_at) >= ` + add(since)
		}
		return q + ` ORDER BY ct.id`, args
	case "fanout":
		q := `SELECT f.id, f.chat_id, f.query, f.position
		      FROM fanout f JOIN chat c ON c.id = f.chat_id`
		if since != "" {
			q += ` WHERE date(c.created_at) >= ` + add(since)
		}
		return q + ` ORDER BY f.id`, args
	case "usage":
		q := `SELECT target_id, day, calls, input_tokens, output_tokens FROM usage_day`
		if since != "" {
			q += ` WHERE day >= ` + add(since)
		}
		return q + ` ORDER BY day, target_id`, args
	default:
		// Unreachable: tables is a fixed list in this package.
		return `SELECT 1 WHERE 0`, nil
	}
}

// scanRow reads one row into a map keyed by column name.
func scanRow(rows *sql.Rows, cols []string) (map[string]any, error) {
	holders := make([]any, len(cols))
	values := make([]sql.RawBytes, len(cols))
	for i := range holders {
		holders[i] = &values[i]
	}
	if err := rows.Scan(holders...); err != nil {
		return nil, err
	}

	out := make(map[string]any, len(cols))
	for i, name := range cols {
		if values[i] == nil {
			out[name] = nil
			continue
		}
		out[name] = coerce(name, string(values[i]))
	}
	return out, nil
}

// coerce turns a SQLite text value into the JSON type a consumer expects.
//
// SQLite is loosely typed and the driver hands back bytes, so an id would
// otherwise export as a string and a boolean as "1". Both would force every
// consumer to guess.
func coerce(column, raw string) any {
	switch column {
	case "branded", "active", "online", "enabled":
		return raw == "1" || strings.EqualFold(raw, "true")
	case "tags":
		// Stored as a JSON array. Re-emitting it as a string would make a
		// consumer parse JSON out of JSON.
		var tags []string
		if json.Unmarshal([]byte(raw), &tags) == nil {
			return nonNil(tags)
		}
		return raw
	}
	if isNumericColumn(column) {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	}
	return raw
}

func isNumericColumn(name string) bool {
	switch name {
	case "id", "chat_id", "competitor_id", "prompt_id", "target_id", "evaluation_id",
		"offset_start", "offset_end", "list_rank", "position",
		"input_tokens", "output_tokens", "calls", "planned", "completed", "failed":
		return true
	}
	return false
}

// csvCell renders one value for a CSV file. A null is an empty cell rather
// than the word "null", because a spreadsheet reads that as text.
func csvCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(t, 10)
	case []string:
		return strings.Join(t, "|")
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func writeValue(bw *errWriter, v any) error {
	if bw.err != nil {
		return bw.err
	}
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	return bw.err
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// errWriter carries the first write error so the streaming path does not need
// a check after every fragment.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	// json.Encoder.Encode appends a newline after every value. Inside a
	// stream that would put a line break between every array element, so it
	// is trimmed here rather than by buffering the whole value.
	p = trimTrailingNewline(p)
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	if _, err := fmt.Fprintf(e.w, format, args...); err != nil {
		e.err = err
	}
}

func trimTrailingNewline(p []byte) []byte {
	if n := len(p); n > 0 && p[n-1] == '\n' {
		return p[:n-1]
	}
	return p
}

// writeCSVFile writes one small table in full. Used for the property, which
// is a single row and does not need the streaming path.
func writeCSVFile(path string, header []string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
