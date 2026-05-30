package onvif

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- SnapshotProviderImpl tests ---

func TestSnapshotProvider_ImplementsInterface(t *testing.T) {
	t.Helper()
	var _ SnapshotProvider = (*SnapshotProviderImpl)(nil)
}

func TestMockSnapshotProvider_ImplementsInterface(t *testing.T) {
	t.Helper()
	var _ SnapshotProvider = (*MockSnapshotProvider)(nil)
}

func TestMockSnapshotProvider_GetSnapshotUri(t *testing.T) {
	t.Helper()
	m := &MockSnapshotProvider{URI: "http://camera/snapshot.jpg"}
	ctx := context.Background()

	uri, err := m.GetSnapshotUri(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://camera/snapshot.jpg", uri)
	require.Equal(t, 1, m.GetSnapshotUriCalls)
}

func TestMockSnapshotProvider_GetSnapshotUri_Error(t *testing.T) {
	t.Helper()
	m := &MockSnapshotProvider{URI: "", Error: errTestNotFound}
	ctx := context.Background()

	_, err := m.GetSnapshotUri(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
	require.Equal(t, 1, m.GetSnapshotUriCalls)
}

func TestMockSnapshotProvider_CallTracking(t *testing.T) {
	t.Helper()
	m := &MockSnapshotProvider{URI: "http://camera/snap.jpg"}
	ctx := context.Background()

	_, _ = m.GetSnapshotUri(ctx)
	_, _ = m.GetSnapshotUri(ctx)
	_, _ = m.GetSnapshotUri(ctx)

	require.Equal(t, 3, m.GetSnapshotUriCalls)
}

// Note: SnapshotProviderImpl.GetSnapshotUri calls s.client.GetSnapshotURI which
// requires a real onvif-go client. We test this via MockSnapshotProvider in the
// API layer tests. Direct impl testing would need httptest server mocking of the
// onvif-go client's internal SOAP calls, which is not feasible without unsafe hacks.
