package timelapse

import (
	"net/url"
	"path"
	"strings"
)

// DeriveSnapshotURL attempts to derive an HTTP snapshot URL from a camera's
// stream URL and protocol. Returns the derived URL, or empty string if no
// derivation is possible (caller should fall back to other snapshot sources).
//
// Derivation rules:
//   - RTSP (rtsp://...): tries common HTTP snapshot endpoints
//   - ONVIF: returns empty (callers should use GetSnapshotURI from ONVIF client)
//   - HTTP (http://...): returns the URL as-is (already an HTTP endpoint)
//   - Xiaomi: returns empty (no snapshot support)
//   - Unknown protocol: returns empty
func DeriveSnapshotURL(streamURL, protocol string) string {
	if streamURL == "" || protocol == "" {
		return ""
	}

	switch protocol {
	case "xiaomi":
		// Xiaomi cameras do not support HTTP snapshots.
		return ""

	case "onvif":
		// ONVIF cameras should use GetSnapshotURI from the ONVIF client.
		// This is a placeholder — callers should use the ONVIF client directly.
		return ""

	case "http", "http_jpeg":
		// HTTP cameras already serve frames via HTTP; return URL as-is.
		return streamURL

	case "rtsp", "rtsp_h264", "rtsp_h265", "rtsp_mjpeg":
		return deriveSnapshotFromRTSP(streamURL)

	default:
		return ""
	}
}

// deriveSnapshotFromRTSP converts an RTSP URL into HTTP snapshot candidate URLs.
// It extracts the host and port from the RTSP URL and replaces the scheme/port
// with HTTP equivalents, keeping the host port if it's not the default RTSP port.
func deriveSnapshotFromRTSP(rtspURL string) string {
	u, err := url.Parse(rtspURL)
	if err != nil {
		return ""
	}

	// Must have a hostname
	if u.Hostname() == "" {
		return ""
	}

	// Build the base HTTP URL from the RTSP host:port
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	// Try common snapshot paths, returning the first that matches common patterns.
	// We return a list of candidates (comma-separated) so the caller can try each.
	// Common RTSP camera snapshot endpoints:
	endpoints := []string{
		"http://" + host + "/cgi-bin/snapshot.cgi",
		"http://" + host + "/cgi-bin/snapshot",
		"http://" + host + "/snapshot.jpg",
		"http://" + host + "/snapshot",
		"http://" + host + "/?snap=1",
	}

	// If the RTSP URL has a path, try appending snapshot endpoints relative to it.
	p := path.Clean(u.Path)
	if p != "" && p != "/" && p != "." {
		dir := path.Dir(p)
		endpoints = append(
			endpoints,
			"http://"+host+dir+"/snapshot.jpg",
			"http://"+host+dir+"/snapshot",
		)
	}

	// Return the most likely candidate first.
	return endpoints[0]
}

// SnapshotCandidates returns all possible HTTP snapshot URLs derived from an RTSP URL,
// ordered by likelihood. Returns nil if no candidates can be derived.
func SnapshotCandidates(streamURL, protocol string) []string {
	if streamURL == "" || protocol == "" {
		return nil
	}

	switch protocol {
	case "rtsp", "rtsp_h264", "rtsp_h265", "rtsp_mjpeg":
		return snapshotCandidatesFromRTSP(streamURL)
	case "http", "http_jpeg":
		return []string{streamURL}
	default:
		return nil
	}
}

// snapshotCandidatesFromRTSP generates all possible HTTP snapshot candidates
// from an RTSP URL.
func snapshotCandidatesFromRTSP(rtspURL string) []string {
	u, err := url.Parse(rtspURL)
	if err != nil {
		return nil
	}
	if u.Hostname() == "" {
		return nil
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	var candidates []string

	// Common snapshot endpoints
	candidates = append(
		candidates,
		"http://"+host+"/cgi-bin/snapshot.cgi",
		"http://"+host+"/cgi-bin/snapshot",
		"http://"+host+"/snapshot.jpg",
		"http://"+host+"/snapshot",
		"http://"+host+"/?snap=1",
	)

	// Path-relative candidates
	p := path.Clean(u.Path)
	if p != "" && p != "/" && p != "." {
		dir := path.Dir(p)
		candidates = append(
			candidates,
			"http://"+host+dir+"/snapshot.jpg",
			"http://"+host+dir+"/snapshot",
		)
	}

	return candidates
}
