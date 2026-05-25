package health

import (
	"testing"
	"time"
)

func TestHealthScore_Recording(t *testing.T) {
	t.Helper()
	// base=100, no modifiers → score=100
	result := ComputeHealthScore("recording", 0, 0, 100.0)
	if result.Score != 100 {
		t.Errorf("expected score 100, got %d", result.Score)
	}
	if result.Status != "recording" {
		t.Errorf("expected status recording, got %s", result.Status)
	}
	if len(result.Factors) != 0 {
		t.Errorf("expected 0 factors, got %d", len(result.Factors))
	}
}

func TestHealthScore_Reconnecting_5min(t *testing.T) {
	t.Helper()
	// base=50, modifier -20 (offline>5min) → score=30
	result := ComputeHealthScore("reconnecting", 6*time.Minute, 0, 100.0)
	if result.Score != 30 {
		t.Errorf("expected score 30, got %d", result.Score)
	}
	if len(result.Factors) != 1 {
		t.Fatalf("expected 1 factor, got %d", len(result.Factors))
	}
	if result.Factors[0].Name != "offline_duration" {
		t.Errorf("expected factor name offline_duration, got %s", result.Factors[0].Name)
	}
	if result.Factors[0].Impact != -20 {
		t.Errorf("expected factor impact -20, got %d", result.Factors[0].Impact)
	}
}

func TestHealthScore_Reconnecting_1hour(t *testing.T) {
	t.Helper()
	// base=50, modifier -20 (offline>5min) + -30 (offline>30min) → score=0
	result := ComputeHealthScore("reconnecting", 1*time.Hour, 0, 100.0)
	if result.Score != 0 {
		t.Errorf("expected score 0, got %d", result.Score)
	}
	// Should have 2 factors: offline>5min and offline>30min
	factorCount := len(result.Factors)
	if factorCount != 2 {
		t.Fatalf("expected 2 factors, got %d", len(result.Factors))
	}
	// Verify cumulative impact
	var totalImpact int
	for _, f := range result.Factors {
		totalImpact += f.Impact
	}
	if totalImpact != -50 {
		t.Errorf("expected total impact -50, got %d", totalImpact)
	}
}

func TestHealthScore_Error(t *testing.T) {
	t.Helper()
	// base=0 → score=0
	result := ComputeHealthScore("error", 0, 0, 100.0)
	if result.Score != 0 {
		t.Errorf("expected score 0, got %d", result.Score)
	}
}

func TestHealthScore_WithRecentAnomalies(t *testing.T) {
	t.Helper()
	// base=100, modifier -15 (3+ anomalies in last hour) → score=85
	result := ComputeHealthScore("recording", 0, 5, 100.0)
	if result.Score != 85 {
		t.Errorf("expected score 85, got %d", result.Score)
	}
	// Find the anomaly factor
	found := false
	for _, f := range result.Factors {
		if f.Name == "recent_anomalies" {
			found = true
			if f.Impact != -15 {
				t.Errorf("expected anomaly impact -15, got %d", f.Impact)
			}
		}
	}
	if !found {
		t.Error("expected recent_anomalies factor")
	}
}

func TestHealthScore_ManyAnomalies(t *testing.T) {
	t.Helper()
	// base=100, modifier -25 (10+ anomalies in last hour) → score=75
	result := ComputeHealthScore("recording", 0, 12, 100.0)
	if result.Score != 75 {
		t.Errorf("expected score 75, got %d", result.Score)
	}
}

func TestHealthScore_LowUptime(t *testing.T) {
	t.Helper()
	// base=100, modifier -10 (uptime<95%) → score=90
	result := ComputeHealthScore("recording", 0, 0, 90.0)
	if result.Score != 90 {
		t.Errorf("expected score 90, got %d", result.Score)
	}
	found := false
	for _, f := range result.Factors {
		if f.Name == "low_uptime" {
			found = true
			if f.Impact != -10 {
				t.Errorf("expected low_uptime impact -10, got %d", f.Impact)
			}
		}
	}
	if !found {
		t.Error("expected low_uptime factor")
	}
}

func TestHealthScore_VeryLowUptime(t *testing.T) {
	t.Helper()
	// base=100, modifier -20 (uptime<80%) → score=80
	result := ComputeHealthScore("recording", 0, 0, 70.0)
	if result.Score != 80 {
		t.Errorf("expected score 80, got %d", result.Score)
	}
}

func TestHealthScore_CombinedFactors(t *testing.T) {
	t.Helper()
	// base=50 (reconnecting), -20 (offline>5min), -30 (offline>30min), -15 (anomalies>3), -10 (uptime<95%)
	// = 50 - 20 - 30 - 15 - 10 = -25, clamped to 0
	result := ComputeHealthScore("reconnecting", 45*time.Minute, 5, 90.0)
	if result.Score != 0 {
		t.Errorf("expected score 0 (clamped), got %d", result.Score)
	}
}

func TestHealthScore_ScoreNeverExceeds100(t *testing.T) {
	t.Helper()
	// Even with status "stopped" giving base=100, ensure no overflow
	result := ComputeHealthScore("stopped", 0, 0, 100.0)
	if result.Score > 100 {
		t.Errorf("score should never exceed 100, got %d", result.Score)
	}
}

func TestHealthScore_UnknownStatus(t *testing.T) {
	t.Helper()
	// Unknown status gets base=50 (cautious default)
	result := ComputeHealthScore("unknown_status", 0, 0, 100.0)
	if result.Score != 50 {
		t.Errorf("expected score 50 for unknown status, got %d", result.Score)
	}
}
