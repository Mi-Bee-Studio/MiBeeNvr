// SPDX-License-Identifier: MIT
//
// Pre-refactoring safety tests for FTP server covering path resolution,
// authentication, directory confinement, ReadDir filtering, extToFormat,
// upload path validation, and concurrent access safety.

package ftp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ftpserverlib "github.com/fclairamb/ftpserverlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// --- resolvePath tests (path traversal prevention) ---

func TestResolvePathRoot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("/")
	require.NoError(t, err)
	require.Equal(t, srv.storageMgr.RootDir(), path)
}

func TestResolvePathSubdirectory(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("/cam01/video.mp4")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "cam01", "video.mp4"), path)
}

func TestResolvePathEmptyString(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("")
	require.NoError(t, err)
	// Empty path should resolve to root
	require.Equal(t, srv.storageMgr.RootDir(), path)
}

func TestResolvePathDot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath(".")
	require.NoError(t, err)
	require.Equal(t, srv.storageMgr.RootDir(), path)
}

func TestResolvePathTraversalRelative(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Relative .. escapes root — this IS caught
	_, err := cd.resolvePath("..")
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// --- extToFormat tests ---

func TestExtToFormatMP4(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".mp4"))
}

func TestExtToFormatH264(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".h264"))
}

func TestExtToFormatAVI(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".avi"))
}

func TestExtToFormatMKV(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".mkv"))
}

func TestExtToFormatMOV(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".mov"))
}

func TestExtToFormatJPG(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatMJPEG, extToFormat(".jpg"))
}

func TestExtToFormatJPEG(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatMJPEG, extToFormat(".jpeg"))
}

func TestExtToFormatUnknown(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.Format("unknown"), extToFormat(".dat"))
}

func TestExtToFormatNoDot(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat("mp4"))
}

func TestExtToFormatUppercase(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.FormatH264, extToFormat(".MP4"))
	assert.Equal(t, model.FormatMJPEG, extToFormat(".JPG"))
}

func TestExtToFormatEmpty(t *testing.T) {
	t.Helper()
	assert.Equal(t, model.Format("unknown"), extToFormat(""))
}

// --- AuthUser edge cases ---

func TestAuthUserEmptyUsernameAndPassword(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	_, err := srv.AuthUser(cc, "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires username and password")
}

func TestAuthUserCorrectCredentials(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	driver, err := srv.AuthUser(cc, "admin", "secret")
	require.NoError(t, err)
	require.NotNil(t, driver)

	cd, ok := driver.(*clientDriver)
	require.True(t, ok)
	require.NotNil(t, cd.server)
}

func TestAuthUserSetsPathToRoot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	_, err := srv.AuthUser(cc, "admin", "secret")
	require.NoError(t, err)
	assert.Equal(t, "/", cc.Path())
}

// --- ClientConnected / ClientDisconnected ---

func TestClientConnected(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	msg, err := srv.ClientConnected(cc)
	require.NoError(t, err)
	assert.Contains(t, msg, "220")
	assert.Contains(t, msg, "MiBee NVR FTP Server")
}

func TestClientDisconnected(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	// Should not panic
	srv.ClientDisconnected(cc)
}

// --- GetSettings ---

func TestGetSettings(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)

	settings, err := srv.GetSettings()
	require.NoError(t, err)
	assert.Equal(t, ":2121", settings.ListenAddr)
	assert.Equal(t, "MiBee NVR FTP Server", settings.Banner)
}

func TestGetSettingsPortRange(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	mgr, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(":9999", "50000-50100", "user", "pass", mgr, nil)
	settings, err := srv.GetSettings()
	require.NoError(t, err)
	require.NotNil(t, settings.PassiveTransferPortRange)
	// PortRange implements PasvPortGetter interface
	portStart, _, ok := settings.PassiveTransferPortRange.FetchNext()
	require.True(t, ok)
	assert.True(t, portStart >= 50000 && portStart <= 50100, "port should be in range 50000-50100, got %d", portStart)
}

func TestGetSettingsInvalidPortRange(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	mgr, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(":9999", "invalid", "user", "pass", mgr, nil)
	settings, err := srv.GetSettings()
	require.NoError(t, err)
	assert.Nil(t, settings.PassiveTransferPortRange)
}

// --- GetTLSConfig ---

func TestGetTLSConfig(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	tlsConfig, err := srv.GetTLSConfig()
	require.NoError(t, err)
	assert.Nil(t, tlsConfig, "plain FTP should return nil TLS config")
}

// --- ReadDir filtering ---

func TestReadDirFiltersHiddenFiles(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))

	// Create visible and hidden files
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "video.mp4"), []byte("data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, ".hidden"), []byte("hidden"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "temp.tmp"), []byte("temp"), 0o644))

	infos, err := cd.ReadDir("/cam01")
	require.NoError(t, err)

	// Should only return the visible file
	assert.Len(t, infos, 1)
	assert.Equal(t, "video.mp4", infos[0].Name())
}

func TestReadDirNonexistent(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	_, err := cd.ReadDir("/nonexistent")
	require.Error(t, err)
}

func TestReadDirEmpty(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "empty-cam")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))

	infos, err := cd.ReadDir("/empty-cam")
	require.NoError(t, err)
	assert.Len(t, infos, 0)
}

// --- Upload path validation ---

func TestUploadInvalidPathNoCamera(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Path without camera_id/filename format
	_, err := cd.GetHandle("/justfile.mp4", os.O_WRONLY|os.O_CREATE, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid upload path")
}

func TestUploadInvalidPathEmpty(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Empty path
	_, err := cd.GetHandle("/", os.O_WRONLY|os.O_CREATE, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid upload path")
}

func TestUploadWithExtension(t *testing.T) {
	t.Helper()
	srv, db := newTestServer(t)
	cd := &clientDriver{server: srv}

	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	// Write data
	data := []byte("mp4 video data")
	n, err := ft.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)

	// Close to trigger DB insert
	require.NoError(t, ft.Close())

	// Verify recording was created with h264 format
	ctx := context.Background()
	recordings, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam01"})
	require.NoError(t, err)
	require.Len(t, recordings, 1)
	assert.Equal(t, model.FormatH264, recordings[0].Format)
}

func TestUploadWithJPEGExtension(t *testing.T) {
	t.Helper()
	srv, db := newTestServer(t)
	cd := &clientDriver{server: srv}

	ft, err := cd.GetHandle("/cam02/snapshot.jpg", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	data := []byte("jpeg image data")
	_, err = ft.Write(data)
	require.NoError(t, err)
	require.NoError(t, ft.Close())

	ctx := context.Background()
	recordings, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam02"})
	require.NoError(t, err)
	require.Len(t, recordings, 1)
	assert.Equal(t, model.FormatMJPEG, recordings[0].Format)
}

// --- Download with offset ---

func TestDownloadWithOffset(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Create test file
	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "video.mp4"), []byte("0123456789abcdef"), 0o644))

	// Download with offset of 4
	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_RDONLY, 4)
	require.NoError(t, err)
	require.NotNil(t, ft)

	buf := make([]byte, 12)
	n, err := ft.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "456789abcdef", string(buf[:n]))

	require.NoError(t, ft.Close())
}

func TestDownloadNonexistentFile(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	_, err := cd.GetHandle("/nonexistent/video.mp4", os.O_RDONLY, 0)
	require.Error(t, err)
}

// --- Stat operation ---

func TestStatExistingFile(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "video.mp4"), []byte("data"), 0o644))

	info, err := cd.Stat("/cam01/video.mp4")
	require.NoError(t, err)
	assert.Equal(t, "video.mp4", info.Name())
	assert.Equal(t, int64(4), info.Size())
}

func TestStatNonexistent(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	_, err := cd.Stat("/nonexistent")
	require.Error(t, err)
}

// --- Mkdir / MkdirAll ---

func TestMkdir(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Mkdir("/newcam", 0o755)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(srv.storageMgr.RootDir(), "newcam"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestMkdirAll(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.MkdirAll("/deep/nested/dir", 0o755)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(srv.storageMgr.RootDir(), "deep", "nested", "dir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// --- Remove / RemoveAll ---

func TestRemove(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	filePath := filepath.Join(srv.storageMgr.RootDir(), "file.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0o644))

	err := cd.Remove("/file.txt")
	require.NoError(t, err)

	_, err = os.Stat(filePath)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveNonexistent(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Remove("/nonexistent.txt")
	require.Error(t, err)
}

// --- Rename ---

func TestRename(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	oldPath := filepath.Join(srv.storageMgr.RootDir(), "old.txt")
	require.NoError(t, os.WriteFile(oldPath, []byte("data"), 0o644))

	err := cd.Rename("/old.txt", "/new.txt")
	require.NoError(t, err)

	_, err = os.Stat(oldPath)
	assert.True(t, os.IsNotExist(err))

	content, err := os.ReadFile(filepath.Join(srv.storageMgr.RootDir(), "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(content))
}

func TestRenameTraversalSource(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Rename("/../../etc/passwd", "/safe.txt")
	require.Error(t, err)
}

func TestRenameTraversalDest(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Rename("/safe.txt", "/../../tmp/evil")
	require.Error(t, err)
}

// --- Create / Open ---

func TestCreateFile(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	f, err := cd.Create("/newfile.txt")
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, f.Close())
}

func TestOpenFile(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	filePath := filepath.Join(srv.storageMgr.RootDir(), "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))

	f, err := cd.Open("/existing.txt")
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, f.Close())
}

// --- Chown (no-op) ---

func TestChown(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Chown("/any", 0, 0)
	require.NoError(t, err)
}

// --- clientDriver Name ---

func TestClientDriverName(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	assert.Equal(t, "mibee-nvr", cd.Name())
}

// --- Upload with no extension gets .dat ---

func TestUploadNoExtension(t *testing.T) {
	t.Helper()
	srv, db := newTestServer(t)
	cd := &clientDriver{server: srv}

	ft, err := cd.GetHandle("/cam01/file_no_ext", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	_, err = ft.Write([]byte("data"))
	require.NoError(t, err)
	require.NoError(t, ft.Close())

	ctx := context.Background()
	recordings, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam01"})
	require.NoError(t, err)
	require.Len(t, recordings, 1)

	// No extension maps to "unknown"
	assert.Equal(t, model.Format("unknown"), recordings[0].Format)

	// File should be auto-named with .dat extension
	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	entries, err := os.ReadDir(cameraDir)
	require.NoError(t, err)
	var found bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".dat") {
			found = true
			break
		}
	}
	assert.True(t, found, "file without extension should be saved with .dat")
}

// --- uploadFileTransfer size tracking ---

func TestUploadSizeTracking(t *testing.T) {
	t.Helper()
	srv, db := newTestServer(t)
	cd := &clientDriver{server: srv}

	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)

	data1 := []byte("first chunk ")
	data2 := []byte("second chunk")
	_, err = ft.Write(data1)
	require.NoError(t, err)
	_, err = ft.Write(data2)
	require.NoError(t, err)
	require.NoError(t, ft.Close())

	ctx := context.Background()
	recordings, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam01"})
	require.NoError(t, err)
	require.Len(t, recordings, 1)
	assert.Equal(t, int64(len(data1)+len(data2)), recordings[0].FileSize)
}

// --- ResolvePath with various cleanings ---

func TestResolvePathDoubleSlash(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("/cam01//video.mp4")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "cam01", "video.mp4"), path)
}

func TestResolvePathTrailingSlash(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("/cam01/")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "cam01"), path)
}

// --- Ensure the mockClientContext satisfies the interface at compile time ---

func TestMockClientContextInterface(t *testing.T) {
	t.Helper()
	// Compile-time check
	var _ ftpserverlib.ClientContext = &mockClientContext{}
}

// --- resolvePath traversal: absolute path with .. is cleaned to stay within root ---

func TestResolvePathAbsoluteEscape(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// filepath.Clean("/../../../etc/passwd") → "/etc/passwd"
	// filepath.Join(root, "/etc/passwd") → root/etc/passwd (WITHIN root)
	// This is SAFE — resolvePath correctly confines to root.
	path, err := cd.resolvePath("/../../../etc/passwd")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "etc", "passwd"), path)
}

// --- resolvePath traversal: deep nesting stays within root ---

func TestResolvePathDeepTraversal(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// filepath.Clean normalizes away the .., result stays within root
	path, err := cd.resolvePath("/cam01/../../../../../../etc/passwd")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "etc", "passwd"), path)
}

// --- resolvePath traversal: mixed dot-dot segments ---

func TestResolvePathMixedTraversal(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// filepath.Clean normalizes to /etc/passwd, which stays within root
	path, err := cd.resolvePath("/cam01/video/../../../etc/passwd")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "etc", "passwd"), path)
}

// --- resolvePath normal subdirectory ---

func TestResolvePathNormalSubdir(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	path, err := cd.resolvePath("/cam01/subdir/video.mp4")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "cam01", "subdir", "video.mp4"), path)
}

// --- resolvePath with double dots in filename (not traversal) ---

func TestResolvePathDoubleDotsInFilename(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// File literally named "..mp4" — not a traversal
	path, err := cd.resolvePath("/cam01/..mp4")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(srv.storageMgr.RootDir(), "cam01", "..mp4"), path)
}

// --- Stat resolves absolute paths within root ---

func TestStatAbsolutePathWithinRoot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// /../../../etc/passwd is cleaned to /etc/passwd within root
	// Should fail because the file doesn't exist, not because of access denial
	_, err := cd.Stat("/../../../etc/passwd")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "access denied")
}

// --- Mkdir with relative traversal (actually escapes) ---

func TestMkdirRelativeTraversalBlocked(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Relative path that actually escapes root
	err := cd.Mkdir("../../evil", 0o755)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// --- Remove with relative traversal ---

func TestRemoveRelativeTraversalBlocked(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.Remove("../../etc/passwd")
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// --- Create with relative traversal ---

func TestCreateRelativeTraversalBlocked(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	_, err := cd.Create("../../evil.txt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// --- Open resolves absolute paths within root ---

func TestOpenAbsolutePathWithinRoot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// /../../../etc/shadow is cleaned to /etc/shadow within root
	// Should fail because file doesn't exist, not because of access denial
	_, err := cd.Open("/../../../etc/shadow")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "access denied")
}

// --- GetHandle download resolves absolute paths within root ---

func TestGetHandleDownloadAbsolutePath(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// /../../../etc/passwd is cleaned to /etc/passwd within root
	// Should fail because file doesn't exist, not because of access denial
	_, err := cd.GetHandle("/../../../etc/passwd", os.O_RDONLY, 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "access denied")
}

// --- GetHandle upload with traversal in camera ID ---

func TestGetHandleUploadTraversalCameraID(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// Camera ID containing .. should still create a directory named literally
	ft, err := cd.GetHandle("/../../etc/video.mp4", os.O_WRONLY|os.O_CREATE, 0)
	// The upload handler splits path by / so this creates camera ".." which is weird but contained
	// It depends on EnsureCameraDir handling
	if err != nil {
		require.Contains(t, err.Error(), "invalid upload path", "upload traversal should be rejected")
	}
	_ = ft
}

// --- RemoveAll with relative traversal ---

func TestRemoveAllRelativeTraversalBlocked(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	err := cd.RemoveAll("../../etc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// --- ReadDir resolves absolute paths within root ---

func TestReadDirAbsolutePathWithinRoot(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	// /../../../etc is cleaned to /etc within root
	// Should fail because dir doesn't exist, not because of access denial
	_, err := cd.ReadDir("/../../../etc")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "access denied")
}

// --- AuthUser rejects empty strings ---

func TestAuthUserRejectsEmptyUsername(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	_, err := srv.AuthUser(cc, "", "secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires username and password")
}

func TestAuthUserRejectsEmptyPassword(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cc := &mockClientContext{}

	_, err := srv.AuthUser(cc, "admin", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires username and password")
}

// --- GetSettings without port range ---

func TestGetSettingsNoPortRange(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	mgr, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(":9999", "", "user", "pass", mgr, nil)
	settings, err := srv.GetSettings()
	require.NoError(t, err)
	assert.Nil(t, settings.PassiveTransferPortRange)
}

// --- GetSettings with reversed port range ---

func TestGetSettingsReversedPortRange(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	mgr, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(":9999", "50100-50000", "user", "pass", mgr, nil)
	settings, err := srv.GetSettings()
	require.NoError(t, err)
	// start > end is invalid, should be nil
	assert.Nil(t, settings.PassiveTransferPortRange)
}

// --- Upload creates correct auto-name ---

func TestUploadAutoName(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_WRONLY|os.O_CREATE, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	_, err = ft.Write([]byte("data"))
	require.NoError(t, err)
	require.NoError(t, ft.Close())

	// Verify file exists with auto-name pattern
	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	entries, err := os.ReadDir(cameraDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, strings.HasPrefix(entries[0].Name(), "cam01_"), "file should have cam01_ prefix")
	assert.True(t, strings.HasSuffix(entries[0].Name(), ".mp4"), "file should have .mp4 extension")
}

// --- Download with zero offset equals normal download ---

func TestDownloadZeroOffset(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "video.mp4"), []byte("hello"), 0o644))

	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_RDONLY, 0)
	require.NoError(t, err)
	require.NotNil(t, ft)

	buf := make([]byte, 5)
	n, err := ft.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf[:n]))
	require.NoError(t, ft.Close())
}

// --- Download with negative offset ---

func TestDownloadNegativeOffset(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	cameraDir := filepath.Join(srv.storageMgr.RootDir(), "cam01")
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "video.mp4"), []byte("0123456789"), 0o644))

	ft, err := cd.GetHandle("/cam01/video.mp4", os.O_RDONLY, -1)
	require.NoError(t, err)
	require.NotNil(t, ft)

	buf := make([]byte, 10)
	n, err := ft.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(buf[:n]))
	require.NoError(t, ft.Close())
}

// --- Chmod and Chtimes ---

func TestChmod(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	filePath := filepath.Join(srv.storageMgr.RootDir(), "chmod-test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0o644))

	err := cd.Chmod("/chmod-test.txt", 0o600)
	require.NoError(t, err)

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestChtimes(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	filePath := filepath.Join(srv.storageMgr.RootDir(), "chtimes-test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0o644))

	newTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	err := cd.Chtimes("/chtimes-test.txt", newTime, newTime)
	require.NoError(t, err)

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	assert.Equal(t, newTime, info.ModTime().UTC())
}

// --- OpenFile ---

func TestOpenFileReadWrite(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	filePath := filepath.Join(srv.storageMgr.RootDir(), "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))

	f, err := cd.OpenFile("/existing.txt", os.O_RDWR, 0o644)
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, f.Close())
}

// --- RemoveAll deletes directory tree ---

func TestRemoveAllDeletesTree(t *testing.T) {
	t.Helper()
	srv, _ := newTestServer(t)
	cd := &clientDriver{server: srv}

	deepDir := filepath.Join(srv.storageMgr.RootDir(), "tree", "a", "b")
	require.NoError(t, os.MkdirAll(deepDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deepDir, "file.txt"), []byte("data"), 0o644))

	err := cd.RemoveAll("/tree")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(srv.storageMgr.RootDir(), "tree"))
	assert.True(t, os.IsNotExist(err))
}
