package timelapse

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)
type mockRecordingLister struct{}

func (m *mockRecordingLister) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	return nil, nil
}

type mockMergeStatusUpdater struct{}

func (m *mockMergeStatusUpdater) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	return nil
}

func TestNewDailyMergeManager(t *testing.T) {
	t.Helper()
	m := NewDailyMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data")
	if m == nil {
		t.Fatal("expected non-nil DailyMergeManager")
	}
}

func TestDailyMergeManager_Run(t *testing.T) {
	t.Helper()
	m := NewDailyMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data")
	err := m.Run(context.Background(), "test-cam", "2026-01-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}