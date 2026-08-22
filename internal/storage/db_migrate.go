package storage

// db_migrate.go — live-database support for storage migration (#395 rework).
// The database itself does NOT move (it lives on the data volume, decoupled
// from the recording root); migration only rewrites the stored file paths.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RewritePathPrefix rewrites every path-ish column in every user table from
// the oldRoot prefix to newRoot, returning the number of changed rows. Runs
// on the LIVE database (WAL + busy_timeout absorb concurrent recorder
// inserts). Generic on purpose: recordings (file_path, merge_path), merge
// outputs, snapshots, transcode tasks — anything storing an absolute path
// under the recording root moves with it.
func (d *DB) RewritePathPrefix(ctx context.Context, oldRoot, newRoot string) (int64, error) {
	return d.withConn(ctx, func(conn *sql.Conn) (int64, error) {
		var tables []string
		rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return 0, err
			}
			tables = append(tables, name)
		}
		rows.Close()

		pattern := likeEscape(oldRoot) + `/%`
		var total int64
		for _, table := range tables {
			info, err := conn.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
			if err != nil {
				return total, err
			}
			var pathCols []string
			for info.Next() {
				var cid int
				var name, colType string
				var notNull, pk int
				var dflt sql.NullString
				if err := info.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
					info.Close()
					return total, err
				}
				if strings.HasSuffix(name, "_path") || name == "path" {
					pathCols = append(pathCols, name)
				}
			}
			info.Close()
			for _, col := range pathCols {
				q := fmt.Sprintf(
					`UPDATE %s SET %s = %s || substr(%s, %d) WHERE %s LIKE %s ESCAPE '\'`,
					quoteIdent(table), quoteIdent(col), sqlString(newRoot), quoteIdent(col),
					len(oldRoot)+1, quoteIdent(col), sqlString(pattern))
				res, err := conn.ExecContext(ctx, q)
				if err != nil {
					return total, fmt.Errorf("%s.%s: %w", table, col, err)
				}
				if n, err := res.RowsAffected(); err == nil {
					total += n
				}
			}
		}
		return total, nil
	})
}

// CountPathPrefix returns how many recordings rows still reference the given
// root prefix — the migration verification query (0 = fully rewritten).
func (d *DB) CountPathPrefix(ctx context.Context, root string) (int64, error) {
	var n int64
	_, err := d.withConn(ctx, func(conn *sql.Conn) (int64, error) {
		row := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM recordings WHERE file_path LIKE ? ESCAPE '\'`,
			likeEscape(root)+`/%`)
		return 0, row.Scan(&n)
	})
	return n, err
}

// DistinctTopSegments returns the distinct first path segments (camera
// directories) that recordings rows reference under root.
func (d *DB) DistinctTopSegments(ctx context.Context, root string) ([]string, error) {
	var segs []string
	_, err := d.withConn(ctx, func(conn *sql.Conn) (int64, error) {
		rows, err := conn.QueryContext(ctx, `SELECT DISTINCT file_path FROM recordings`)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		prefix := root + "/"
		seen := map[string]bool{}
		for rows.Next() {
			var file string
			if err := rows.Scan(&file); err != nil {
				return 0, err
			}
			if !strings.HasPrefix(file, prefix) {
				continue
			}
			rest := file[len(prefix):]
			idx := strings.IndexByte(rest, '/')
			if idx <= 0 {
				continue
			}
			seg := rest[:idx]
			if !seen[seg] {
				seen[seg] = true
				segs = append(segs, seg)
			}
		}
		return 0, rows.Err()
	})
	return segs, err
}

// withConn runs fn on a dedicated write-pool connection (single-writer pool).
func (d *DB) withConn(ctx context.Context, fn func(*sql.Conn) (int64, error)) (int64, error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return fn(conn)
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// MigratableRec mirrors migration.MigratableRecording without an import
// cycle (the migration package consumes this shape via its DB interface).
type MigratableRec = struct {
	ID        string
	FilePath  string
	MergePath string
	FileSize  int64
}

// ListMigratableRecordings returns one camera's rows whose file_path (or
// merge_path) still lives outside keepUnder — the backlog the background
// migrator walks, oldest first.
func (d *DB) ListMigratableRecordings(ctx context.Context, cameraID, keepUnder string) ([]MigratableRec, error) {
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT id, file_path, COALESCE(merge_path, ''), COALESCE(file_size, 0)
		FROM recordings
		WHERE camera_id = ?
		  AND (file_path NOT LIKE ? ESCAPE '\' OR (merge_path IS NOT NULL AND merge_path != '' AND merge_path NOT LIKE ? ESCAPE '\'))
		ORDER BY started_at ASC`,
		cameraID, likeEscape(keepUnder)+`/%`, likeEscape(keepUnder)+`/%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MigratableRec
	for rows.Next() {
		var r MigratableRec
		if err := rows.Scan(&r.ID, &r.FilePath, &r.MergePath, &r.FileSize); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RewriteRecordingPaths rewrites ONE recording row's paths (the per-file
// transaction of the background migrator: copy → rewrite → delete source).
func (d *DB) RewriteRecordingPaths(ctx context.Context, id, newFile string, newMerge sql.NullString) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET file_path = ?, merge_path = ? WHERE id = ?`,
		newFile, newMerge, id)
	return err
}

// ListCameraIDs returns every camera that has at least one recording row —
// the batch-migration enumerator.
func (d *DB) ListCameraIDs(ctx context.Context) ([]string, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT DISTINCT camera_id FROM recordings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SumMigratableBytes returns the total file_size of one camera's rows whose
// files still live outside keepUnder — the synchronous capacity precheck.
func (d *DB) SumMigratableBytes(ctx context.Context, cameraID, keepUnder string) (int64, error) {
	var n sql.NullInt64
	_, err := d.withConn(ctx, func(conn *sql.Conn) (int64, error) {
		row := conn.QueryRowContext(ctx, `
			SELECT SUM(COALESCE(file_size, 0)) FROM recordings
			WHERE camera_id = ? AND file_path NOT LIKE ? ESCAPE '\'`,
			cameraID, likeEscape(keepUnder)+`/%`)
		return 0, row.Scan(&n)
	})
	return n.Int64, err
}
