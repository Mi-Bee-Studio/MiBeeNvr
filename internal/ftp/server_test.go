package ftp

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ftpserverlib "github.com/fclairamb/ftpserverlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// mockClientContext implements ftpserverlib.ClientContext for testing.
type mockClientContext struct {
	path    string
	debug   bool
	extra   any
	lastCmd string
	lastDC  ftpserverlib.DataChannel
}

func (m *mockClientContext) Path() string         { return m.path }
func (m *mockClientContext) SetPath(v string)     { m.path = v }
func (m *mockClientContext) SetListPath(v string) {}
func (m *mockClientContext) SetDebug(d bool)      { m.debug = d }
func (m *mockClientContext) Debug() bool          { return m.debug }
func (m *mockClientContext) ID() uint32           { return 1 }
func (m *mockClientContext) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

func (m *mockClientContext) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2121}
}

func (m *mockClientContext) GetClientVersion() string { return "test-client" }
func (m *mockClientContext) Close() error             { return nil }
func (m *mockClientContext) HasTLSForControl() bool   { return false }
func (m *mockClientContext) HasTLSForTransfers() bool { return false }
func (m *mockClientContext) GetLastCommand() string   { return m.lastCmd }

func (m *mockClientContext) GetLastDataChannel() ftpserverlib.DataChannel          { return m.lastDC }
func (m *mockClientContext) SetTLSRequirement(r ftpserverlib.TLSRequirement) error { return nil }
func (m *mockClientContext) SetExtra(e any)                                        { m.extra = e }
func (m *mockClientContext) Extra() any                                            { return m.extra }

// newTestServer creates a Server with a temp storage root and SQLite DB.
func newTestServer(t *testing.T) (*Server, *storage.DB) {
	t.Helper()
	tmpDir := t.TempDir()

	mgr, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	srv := NewServer(":2121", "50000-50100", "admin", "secret", mgr, db)
	return srv, db
}

func TestNewServer(t *testing.T) {
	srv, _ := newTestServer(t)

	assert.NotNil(t, srv)
	assert.Equal(t, ":2121", srv.addr)
	assert.Equal(t, "50000-50100", srv.portRange)
	assert.Equal(t, "admin", srv.username)
	assert.Equal(t, "secret", srv.password)
	assert.NotNil(t, srv.storageMgr)
	assert.NotNil(t, srv.db)
}

func TestAuthRequired(t *testing.T) {
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	// Empty username should be rejected
	_, err := srv.AuthUser(cc, "", "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires username and password")

	// Empty password should be rejected
	_, err = srv.AuthUser(cc, "admin", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires username and password")

	// Wrong username should be rejected
	_, err = srv.AuthUser(cc, "wrong", "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid credentials")

	// Wrong password should be rejected
	_, err = srv.AuthUser(cc, "admin", "wrong")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid credentials")

	// Both empty should be rejected
	_, err = srv.AuthUser(cc, "", "")
	assert.Error(t, err)

	// Correct credentials should succeed
	driver, err := srv.AuthUser(cc, "admin", "secret")
	assert.NoError(t, err)
	assert.NotNil(t, driver)
	assert.Equal(t, "/", cc.Path(), "path should be set to root after auth")

	// Anonymous should be rejected
	_, err = srv.AuthUser(cc, "anonymous", "")
	assert.Error(t, err)
}

func TestFileUpload(t *testing.T) {
	srv, db := newTestServer(t)
	cc := &mockClientContext{}

	// Authenticate
	driver, err := srv.AuthUser(cc, "admin", "secret")
	require.NoError(t, err)
	require.NotNil(t, driver)

	cd, ok := driver.(*clientDriver)
	require.True(t, ok, "driver should be *clientDriver")

	// Upload a file to /cam01/video.mp4
	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	// Write test data
	data := []byte("test video content for mibee-nvr")
	n, err := ft.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	// Close to finalize (triggers DB insert)
	err = ft.Close()
	require.NoError(t, err)

	// Verify file exists in camera directory with auto-naming
	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	entries, err := os.ReadDir(cameraDir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries), 1, "camera directory should have at least one file")

	// Find and verify the uploaded file
	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cam01_") && strings.HasSuffix(e.Name(), ".mp4") {
			found = true
			fullPath := filepath.Join(cameraDir, e.Name())
			content, readErr := os.ReadFile(fullPath)
			require.NoError(t, readErr)
			assert.Equal(t, data, content, "file content should match uploaded data")
			break
		}
	}
	assert.True(t, found, "uploaded file with auto-naming should exist")

	// Verify DB recording entry was created
	ctx := context.Background()
	recordings, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam01"})
	require.NoError(t, err)
	require.Len(t, recordings, 1, "should have exactly one recording for cam01")

	rec := recordings[0]
	assert.Equal(t, "cam01", rec.CameraID)
	assert.Equal(t, model.FormatH264, rec.Format, "mp4 should map to h264 format")
	assert.Equal(t, int64(len(data)), rec.FileSize)
	assert.NotEqual(t, model.MergeStatusMerged, rec.MergeStatus)
	assert.NotEmpty(t, rec.ID, "recording ID should be a valid UUID")
	assert.False(t, rec.StartedAt.IsZero())
	assert.False(t, rec.EndedAt.IsZero())
	assert.Greater(t, rec.Duration, 0.0)
}

func TestFileDownload(t *testing.T) {
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	// Authenticate
	driver, err := srv.AuthUser(cc, "admin", "secret")
	require.NoError(t, err)
	require.NotNil(t, driver)

	cd, ok := driver.(*clientDriver)
	require.True(t, ok, "driver should be *clientDriver")

	// First, create a test file directly in camera directory
	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))

	testFile := filepath.Join(cameraDir, "existing_video.mp4")
	testContent := []byte("existing video content for download test")
	require.NoError(t, os.WriteFile(testFile, testContent, 0o644))

	// Now test downloading the file using O_RDONLY flags
	ft, err := cd.GetHandle("/cam01/existing_video.mp4", os.O_RDONLY, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	// Verify we can read the file content
	readData := make([]byte, len(testContent))
	n, err := ft.Read(readData)
	require.NoError(t, err)
	assert.Equal(t, len(testContent), n)
	assert.Equal(t, testContent, readData[:n], "downloaded content should match original")

	// Clean up
	err = ft.Close()
	require.NoError(t, err)

	// Verify the file wasn't modified or moved (should remain at original path)
	postDownloadContent, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, postDownloadContent, "original file should remain unchanged")
}
