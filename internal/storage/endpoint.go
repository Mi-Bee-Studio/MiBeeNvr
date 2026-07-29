package storage

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
)

// NormalizeOnvifEndpoint canonicalizes an ONVIF device-service URL so that
// semantically-identical endpoints compare equal. It:
//   - lowercases the scheme and host (case-insensitive per RFC 3986)
//   - elides the default port (:80 for http, :443 for https)
//   - strips trailing slashes
//
// This is the single source of truth for endpoint canonicalization across the
// codebase. The autodiscover package re-exports it (it previously had its own
// private copy); the storage layer uses it for both write-side normalization
// (UpsertCamera stores the canonical form) and read-side dedup matching
// (CameraIDByOnvifEndpoint / CameraExistsByOnvifEndpoint normalize before
// comparing), so that a device discovered as "http://1.2.3.4/onvif/..." matches
// a row stored as "http://1.2.3.4:80/onvif/..." (#175).
//
// If the input is not a valid URL (unexpected for ONVIF XAddrs, but possible
// from malformed firmware), it falls back to a best-effort strings.TrimRight
// so the comparison never panics.
func NormalizeOnvifEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Fallback: at least strip trailing slashes (legacy behavior).
		return strings.TrimRight(raw, "/")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	// Elide default ports.
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	// Reconstruct the canonical form.
	result := scheme + "://" + host
	if port != "" {
		result += ":" + port
	}
	path := strings.TrimRight(u.Path, "/")
	result += path
	return result
}

// endpointExists reports whether any camera row's onvif_endpoint is
// semantically equal to query after canonicalization. It first tries an exact
// SQL match (the common, already-normalized case), then falls back to a
// normalize-and-compare scan of all rows so that legacy rows stored with a
// default port (:80) still match a query without it (#175). The camera count
// is small (tens), so the fallback scan is cheap.
func (d *DB) endpointExists(ctx context.Context, query string) (bool, error) {
	// Fast path: exact match on the (now normalized-on-write) column.
	var c int
	if err := d.readConn().QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE onvif_endpoint=? LIMIT 1`, query).Scan(&c); err != nil {
		return false, err
	}
	if c > 0 {
		return true, nil
	}
	// Fallback: normalize every stored endpoint and compare to the normalized query.
	normQuery := NormalizeOnvifEndpoint(query)
	if normQuery == "" || normQuery == query {
		return false, nil // already tried exact; nothing more to do
	}
	rows, err := d.readConn().QueryContext(ctx, `SELECT onvif_endpoint FROM cameras`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var ep string
		if err := rows.Scan(&ep); err != nil {
			return false, err
		}
		if NormalizeOnvifEndpoint(ep) == normQuery {
			return true, nil
		}
	}
	return false, rows.Err()
}

// cameraIDByEndpoint is the id-returning twin of endpointExists, used by
// CameraIDByOnvifEndpoint so dedup can report WHICH existing camera matched.
// Same fast-path-then-normalize strategy.
func (d *DB) cameraIDByEndpoint(ctx context.Context, query string) (string, bool, error) {
	// Fast path: exact match.
	var id string
	if err := d.readConn().QueryRowContext(ctx, `SELECT id FROM cameras WHERE onvif_endpoint=? LIMIT 1`, query).Scan(&id); err == nil {
		return id, true, nil
	} else if err != sql.ErrNoRows {
		return "", false, err
	}
	// Fallback: normalize-and-compare scan.
	normQuery := NormalizeOnvifEndpoint(query)
	if normQuery == "" || normQuery == query {
		return "", false, nil
	}
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, onvif_endpoint FROM cameras`)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid, ep string
		if err := rows.Scan(&rid, &ep); err != nil {
			return "", false, err
		}
		if NormalizeOnvifEndpoint(ep) == normQuery {
			return rid, true, nil
		}
	}
	return "", false, rows.Err()
}

// CameraEndpointRow is a lightweight row used by the normalize-endpoints repair
// command: it only needs the id, name, and current onvif_endpoint value.
type CameraEndpointRow struct {
	ID       string
	Name     string
	Endpoint string
}

// ListCameraEndpointsForRepair returns id/name/onvif_endpoint for every camera
// (including archived). Used by the `repair normalize-endpoints` CLI to find
// rows whose stored endpoint is not in canonical form.
func (d *DB) ListCameraEndpointsForRepair(ctx context.Context) ([]CameraEndpointRow, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, name, onvif_endpoint FROM cameras`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraEndpointRow
	for rows.Next() {
		var r CameraEndpointRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Endpoint); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

// UpdateCameraOnvifEndpointRaw writes the onvif_endpoint column directly,
// bypassing UpsertCamera's other fields. Used by `repair normalize-endpoints`.
// The value should already be canonical (run it through NormalizeOnvifEndpoint).
func (d *DB) UpdateCameraOnvifEndpointRaw(ctx context.Context, cameraID, endpoint string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET onvif_endpoint=? WHERE id=?`, endpoint, cameraID)
	return err
}
