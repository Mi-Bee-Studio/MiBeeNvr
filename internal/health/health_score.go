package health

import (
	"fmt"
	"time"
)

// HealthScore represents a computed 0-100 health score with factor breakdown.
type HealthScore struct {
	Score   int           `json:"score"`
	Status  string        `json:"status"`
	Factors []ScoreFactor `json:"factors,omitempty"`
}

// ScoreFactor represents a single factor that influenced the health score.
type ScoreFactor struct {
	Name   string `json:"name"`
	Impact int    `json:"impact"`
	Detail string `json:"detail"`
}

// ComputeHealthScore calculates a 0-100 health score for a camera based on
// its current status and recent health metrics. This is a pure function
// with no side effects — easily testable.
//
// Parameters:
//   - status: current recorder status ("recording", "reconnecting", "error", "stopped")
//   - offlineDuration: how long the camera has been offline (0 if recording)
//   - anomalyCount: number of anomalies detected in the last hour
//   - uptimePercent: uptime percentage over the evaluation window
func ComputeHealthScore(status string, offlineDuration time.Duration, anomalyCount int, uptimePercent float64) HealthScore {
	// Determine base score from status
	base := baseScore(status)

	score := base
	var factors []ScoreFactor

	// Modifier: offline duration
	if offlineDuration > 5*time.Minute {
		factors = append(factors, ScoreFactor{
			Name:   "offline_duration",
			Impact: -20,
			Detail: fmt.Sprintf("offline for %s (>5min)", offlineDuration.Round(time.Second)),
		})
		score -= 20
	}
	if offlineDuration > 30*time.Minute {
		factors = append(factors, ScoreFactor{
			Name:   "offline_duration",
			Impact: -30,
			Detail: fmt.Sprintf("offline for %s (>30min)", offlineDuration.Round(time.Second)),
		})
		score -= 30
	}

	// Modifier: recent anomaly count
	if anomalyCount > 10 {
		factors = append(factors, ScoreFactor{
			Name:   "recent_anomalies",
			Impact: -25,
			Detail: fmt.Sprintf("%d anomalies in last hour (>10)", anomalyCount),
		})
		score -= 25
	} else if anomalyCount > 3 {
		factors = append(factors, ScoreFactor{
			Name:   "recent_anomalies",
			Impact: -15,
			Detail: fmt.Sprintf("%d anomalies in last hour (>3)", anomalyCount),
		})
		score -= 15
	}

	// Modifier: uptime percentage
	if uptimePercent < 80 {
		factors = append(factors, ScoreFactor{
			Name:   "low_uptime",
			Impact: -20,
			Detail: fmt.Sprintf("uptime %.1f%% (<80%%)", uptimePercent),
		})
		score -= 20
	} else if uptimePercent < 95 {
		factors = append(factors, ScoreFactor{
			Name:   "low_uptime",
			Impact: -10,
			Detail: fmt.Sprintf("uptime %.1f%% (<95%%)", uptimePercent),
		})
		score -= 10
	}

	// Clamp to [0, 100]
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return HealthScore{
		Score:   score,
		Status:  status,
		Factors: factors,
	}
}

// baseScore returns the starting score for a given status.
func baseScore(status string) int {
	switch status {
	case "recording":
		return 100
	case "reconnecting":
		return 50
	case "error":
		return 0
	case "stopped":
		return 100 // stopped is intentional, not unhealthy
	default:
		return 50 // cautious default for unknown statuses
	}
}
