package health

import (
	"sync"
	"time"
)

// ConnectionQuality holds computed quality metrics for a camera.
type ConnectionQuality struct {
	UptimePercent      float64
	TotalFailures      int
	MTBF               time.Duration
	AvgSessionDuration time.Duration
	LastFailure        time.Time
	CurrentStatus      string
}

// sessionEntry records a single online session.
type sessionEntry struct {
	start    time.Time
	end      time.Time
	duration time.Duration
}

// cameraSession tracks per-camera session and failure data.
type cameraSession struct {
	onlineSince  time.Time
	offlineSince time.Time
	sessions     []sessionEntry
	failures     []time.Time
	isOnline     bool
}

// QualityTracker records online/offline sessions per camera and computes
// rolling quality metrics (uptime %, failure count, MTBF, avg session duration)
// over a configurable time window. All data is kept in-memory.
type QualityTracker struct {
	mu      sync.Mutex
	cameras map[string]*cameraSession
	window  time.Duration
	nowFunc func() time.Time
}

// NewQualityTracker creates a tracker with the given rolling window duration.
func NewQualityTracker(window time.Duration) *QualityTracker {
	return &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  window,
		nowFunc: time.Now,
	}
}

// OnOnline records the start of an online session for a camera.
func (q *QualityTracker) OnOnline(cameraID string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.nowFunc()
	s, exists := q.cameras[cameraID]
	if !exists {
		s = &cameraSession{}
		q.cameras[cameraID] = s
	}

	// If already online, ignore duplicate
	if s.isOnline {
		return
	}

	s.isOnline = true
	s.onlineSince = now
}

// OnOffline records the end of an online session and registers a failure.
func (q *QualityTracker) OnOffline(cameraID string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.nowFunc()
	s, exists := q.cameras[cameraID]
	if !exists || !s.isOnline {
		return
	}

	s.isOnline = false
	s.offlineSince = now
	s.sessions = append(s.sessions, sessionEntry{
		start:    s.onlineSince,
		end:      now,
		duration: now.Sub(s.onlineSince),
	})
	s.failures = append(s.failures, now)
}

// GetQuality computes and returns quality metrics for a camera.
func (q *QualityTracker) GetQuality(cameraID string) ConnectionQuality {
	q.mu.Lock()
	defer q.mu.Unlock()

	s, exists := q.cameras[cameraID]
	if !exists {
		return ConnectionQuality{CurrentStatus: "unknown"}
	}

	now := q.nowFunc()
	windowStart := now.Add(-q.window)

	// Prune old data
	s.prune(windowStart)

	status := "offline"
	if s.isOnline {
		status = "online"
	}

	// Compute uptime: sum of completed session durations within window + current session
	var totalOnline time.Duration
	var sessionCount int
	var totalSessionDur time.Duration

	for _, ses := range s.sessions {
		if ses.end.Before(windowStart) {
			continue
		}
		// Clip session start to windowStart
		clippedStart := ses.start
		if clippedStart.Before(windowStart) {
			clippedStart = windowStart
		}
		dur := ses.end.Sub(clippedStart)
		if dur > 0 {
			totalOnline += dur
		}
		totalSessionDur += ses.duration
		sessionCount++
	}

	// Add current ongoing session
	if s.isOnline {
		clippedStart := s.onlineSince
		if clippedStart.Before(windowStart) {
			clippedStart = windowStart
		}
		dur := now.Sub(clippedStart)
		if dur > 0 {
			totalOnline += dur
		}
		totalSessionDur += now.Sub(s.onlineSince)
		sessionCount++
	}

	// Uptime percentage
	uptimePercent := 0.0
	if q.window > 0 {
		uptimePercent = float64(totalOnline) / float64(q.window) * 100.0
	}
	if uptimePercent > 100.0 {
		uptimePercent = 100.0
	}

	// Count failures in window
	failuresInWindow := 0
	var lastFailure time.Time
	for _, f := range s.failures {
		if !f.Before(windowStart) {
			failuresInWindow++
			if f.After(lastFailure) {
				lastFailure = f
			}
		}
	}

	// MTBF: total time span / (failures - 1)
	mtbf := time.Duration(0)
	if failuresInWindow >= 2 {
		// Find first and last failure in window
		var firstF, lastF time.Time
		for _, f := range s.failures {
			if !f.Before(windowStart) {
				if firstF.IsZero() || f.Before(firstF) {
					firstF = f
				}
				if f.After(lastF) {
					lastF = f
				}
			}
		}
		mtbf = lastF.Sub(firstF) / time.Duration(failuresInWindow-1)
	}

	// Average session duration
	avgSession := time.Duration(0)
	if sessionCount > 0 {
		avgSession = totalSessionDur / time.Duration(sessionCount)
	}

	return ConnectionQuality{
		UptimePercent:      uptimePercent,
		TotalFailures:      failuresInWindow,
		MTBF:               mtbf,
		AvgSessionDuration: avgSession,
		LastFailure:        lastFailure,
		CurrentStatus:      status,
	}
}

// GetAllQuality returns quality metrics for all tracked cameras.
func (q *QualityTracker) GetAllQuality() map[string]ConnectionQuality {
	q.mu.Lock()
	defer q.mu.Unlock()

	result := make(map[string]ConnectionQuality, len(q.cameras))
	for cameraID := range q.cameras {
		// GetQuality acquires the lock, so we inline the computation
		// or unlock temporarily. For simplicity, just compute inline:
		result[cameraID] = q.getQualityLocked(cameraID)
	}
	return result
}

// RemoveCamera removes tracking data for a camera.
func (q *QualityTracker) RemoveCamera(cameraID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cameras, cameraID)
}

// getQualityLocked computes quality without acquiring the lock (caller must hold q.mu).
func (q *QualityTracker) getQualityLocked(cameraID string) ConnectionQuality {
	s, exists := q.cameras[cameraID]
	if !exists {
		return ConnectionQuality{CurrentStatus: "unknown"}
	}

	now := q.nowFunc()
	windowStart := now.Add(-q.window)

	s.prune(windowStart)

	status := "offline"
	if s.isOnline {
		status = "online"
	}

	var totalOnline time.Duration
	var sessionCount int
	var totalSessionDur time.Duration

	for _, ses := range s.sessions {
		if ses.end.Before(windowStart) {
			continue
		}
		clippedStart := ses.start
		if clippedStart.Before(windowStart) {
			clippedStart = windowStart
		}
		dur := ses.end.Sub(clippedStart)
		if dur > 0 {
			totalOnline += dur
		}
		totalSessionDur += ses.duration
		sessionCount++
	}

	if s.isOnline {
		clippedStart := s.onlineSince
		if clippedStart.Before(windowStart) {
			clippedStart = windowStart
		}
		dur := now.Sub(clippedStart)
		if dur > 0 {
			totalOnline += dur
		}
		totalSessionDur += now.Sub(s.onlineSince)
		sessionCount++
	}

	uptimePercent := 0.0
	if q.window > 0 {
		uptimePercent = float64(totalOnline) / float64(q.window) * 100.0
	}
	if uptimePercent > 100.0 {
		uptimePercent = 100.0
	}

	failuresInWindow := 0
	var lastFailure time.Time
	for _, f := range s.failures {
		if !f.Before(windowStart) {
			failuresInWindow++
			if f.After(lastFailure) {
				lastFailure = f
			}
		}
	}

	mtbf := time.Duration(0)
	if failuresInWindow >= 2 {
		var firstF, lastF time.Time
		for _, f := range s.failures {
			if !f.Before(windowStart) {
				if firstF.IsZero() || f.Before(firstF) {
					firstF = f
				}
				if f.After(lastF) {
					lastF = f
				}
			}
		}
		mtbf = lastF.Sub(firstF) / time.Duration(failuresInWindow-1)
	}

	avgSession := time.Duration(0)
	if sessionCount > 0 {
		avgSession = totalSessionDur / time.Duration(sessionCount)
	}

	return ConnectionQuality{
		UptimePercent:      uptimePercent,
		TotalFailures:      failuresInWindow,
		MTBF:               mtbf,
		AvgSessionDuration: avgSession,
		LastFailure:        lastFailure,
		CurrentStatus:      status,
	}
}

// prune removes sessions and failures outside the window.
func (s *cameraSession) prune(windowStart time.Time) {
	// Prune sessions
	n := 0
	for _, ses := range s.sessions {
		if !ses.end.Before(windowStart) {
			s.sessions[n] = ses
			n++
		}
	}
	s.sessions = s.sessions[:n]

	// Prune failures
	n = 0
	for _, f := range s.failures {
		if !f.Before(windowStart) {
			s.failures[n] = f
			n++
		}
	}
	s.failures = s.failures[:n]
}
