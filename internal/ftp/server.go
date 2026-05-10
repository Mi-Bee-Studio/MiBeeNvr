package ftp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ftpserverlib "github.com/fclairamb/ftpserverlib"
	"github.com/google/uuid"
	"github.com/spf13/afero"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slog.Default().With("component", "ftp")

// Server implements an FTP server for camera file uploads using ftpserverlib.
// It satisfies the ftpserverlib.MainDriver interface for authentication and
// delegates file operations to clientDriver (afero.Fs + extensions).
type Server struct {
	addr       string
	portRange  string
	username   string
	password   string
	storageMgr *storage.Manager
	db         *storage.DB
	ftpServer  *ftpserverlib.FtpServer
}

// NewServer creates a new FTP server bound to addr with the given credentials.
// portRange should be in "start-end" format (e.g. "50000-50100") for passive transfers.
func NewServer(addr, portRange, username, password string, storageMgr *storage.Manager, db *storage.DB) *Server {
	return &Server{
		addr:       addr,
		portRange:  portRange,
		username:   username,
		password:   password,
		storageMgr: storageMgr,
		db:         db,
	}
}

// Start starts the FTP server. It blocks until ctx is cancelled or an error occurs.
// On context cancellation the listener is closed for graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	ftpServer := ftpserverlib.NewFtpServer(s)
	s.ftpServer = ftpServer

	if err := ftpServer.Listen(); err != nil {
		return fmt.Errorf("ftp: failed to listen on %s: %w", s.addr, err)
	}

	go func() {
		<-ctx.Done()
		ftpServer.Stop()
	}()

	return ftpServer.Serve()
}

// ---- MainDriver interface (ftpserverlib) ----

// GetSettings returns the server configuration.
func (s *Server) GetSettings() (*ftpserverlib.Settings, error) {
	settings := &ftpserverlib.Settings{
		ListenAddr: s.addr,
		Banner:     "MiBee NVR FTP Server",
	}

	if s.portRange != "" {
		parts := strings.Split(s.portRange, "-")
		if len(parts) == 2 {
			start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 == nil && err2 == nil && start > 0 && end >= start {
				settings.PassiveTransferPortRange = &ftpserverlib.PortRange{Start: start, End: end}
			}
		}
	}

	return settings, nil
}

// ClientConnected returns the welcome message sent before authentication.
func (s *Server) ClientConnected(cc ftpserverlib.ClientContext) (string, error) {
	return "220 Welcome to MiBee NVR FTP Server", nil
}

// ClientDisconnected is called when a client disconnects (even unauthenticated).
func (s *Server) ClientDisconnected(cc ftpserverlib.ClientContext) {}

// AuthUser validates credentials. Only the configured username/password are accepted;
// anonymous access is always rejected.
func (s *Server) AuthUser(cc ftpserverlib.ClientContext, user, pass string) (ftpserverlib.ClientDriver, error) {
	if user == "" || pass == "" {
		return nil, fmt.Errorf("ftp: authentication requires username and password")
	}
	if user != s.username || pass != s.password {
		return nil, fmt.Errorf("ftp: invalid credentials")
	}
	cc.SetPath("/")
	return &clientDriver{server: s}, nil
}

// GetTLSConfig returns nil for plain FTP (no TLS).
func (s *Server) GetTLSConfig() (*tls.Config, error) {
	return nil, nil
}

// ---- clientDriver: afero.Fs + extensions ----

// clientDriver implements ftpserverlib.ClientDriver (extends afero.Fs) plus the
// ClientDriverExtensionFileList and ClientDriverExtentionFileTransfer extensions
// for directory listing and file upload/download.
type clientDriver struct {
	server *Server
}

// resolvePath maps a virtual FTP path to a real filesystem path inside the
// storage root, rejecting any path traversal attempts.
func (d *clientDriver) resolvePath(name string) (string, error) {
	cleanPath := filepath.Clean(name)
	realPath := filepath.Join(d.server.storageMgr.RootDir(), cleanPath)
	rel, err := filepath.Rel(d.server.storageMgr.RootDir(), realPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("ftp: access denied: %s", name)
	}
	return realPath, nil
}

// ---- afero.Fs implementation ----

func (d *clientDriver) Name() string { return "mibee-nvr" }

func (d *clientDriver) Create(name string) (afero.File, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Create(realPath)
	if err != nil {
		return nil, err
	}
	return &fileWrapper{File: f}, nil
}

func (d *clientDriver) Mkdir(name string, perm os.FileMode) error {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return err
	}
	return os.Mkdir(realPath, perm)
}

func (d *clientDriver) MkdirAll(path string, perm os.FileMode) error {
	realPath, err := d.resolvePath(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(realPath, perm)
}

func (d *clientDriver) Open(name string) (afero.File, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(realPath)
	if err != nil {
		return nil, err
	}
	return &fileWrapper{File: f}, nil
}

func (d *clientDriver) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(realPath, flag, perm)
	if err != nil {
		return nil, err
	}
	return &fileWrapper{File: f}, nil
}

func (d *clientDriver) Remove(name string) error {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return err
	}
	return os.Remove(realPath)
}

func (d *clientDriver) Chown(name string, uid, gid int) error {
	return nil // not meaningful for this driver
}

func (d *clientDriver) RemoveAll(path string) error {
	realPath, err := d.resolvePath(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(realPath)
}

func (d *clientDriver) Rename(oldname, newname string) error {
	realOld, err := d.resolvePath(oldname)
	if err != nil {
		return err
	}
	realNew, err := d.resolvePath(newname)
	if err != nil {
		return err
	}
	return os.Rename(realOld, realNew)
}

func (d *clientDriver) Stat(name string) (os.FileInfo, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}
	return os.Stat(realPath)
}

func (d *clientDriver) Chmod(name string, mode os.FileMode) error {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return err
	}
	return os.Chmod(realPath, mode)
}

func (d *clientDriver) Chtimes(name string, atime time.Time, mtime time.Time) error {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return err
	}
	return os.Chtimes(realPath, atime, mtime)
}

// ---- ClientDriverExtensionFileList ----

// ReadDir lists directory contents, filtering out hidden files and .tmp artifacts.
func (d *clientDriver) ReadDir(name string) ([]os.FileInfo, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(realPath)
	if err != nil {
		return nil, err
	}

	infos := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// ---- ClientDriverExtentionFileTransfer ----

// GetHandle handles file uploads and downloads.
//
// Upload (O_WRONLY|O_CREATE): requires path /camera_id/filename.
// The file is auto-named as {camera_id}_{timestamp_ms}.{ext} in the camera's
// storage directory. On close, a DB recording entry is created.
//
// Download (O_RDONLY): serves the file at the given path with optional seek offset.
func (d *clientDriver) GetHandle(name string, flags int, offset int64) (ftpserverlib.FileTransfer, error) {
	if flags == os.O_RDONLY {
		return d.handleDownload(name, offset)
	}
	return d.handleUpload(name)
}

func (d *clientDriver) handleDownload(name string, offset int64) (ftpserverlib.FileTransfer, error) {
	realPath, err := d.resolvePath(name)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(realPath)
	if err != nil {
		return nil, err
	}

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}

	return f, nil
}

func (d *clientDriver) handleUpload(name string) (ftpserverlib.FileTransfer, error) {
	cleanPath := filepath.Clean(name)
	parts := strings.Split(strings.TrimPrefix(cleanPath, "/"), string(filepath.Separator))

	if len(parts) < 2 {
		return nil, fmt.Errorf("ftp: invalid upload path %q, expected /camera_id/filename", name)
	}

	cameraID := parts[0]
	ext := filepath.Ext(parts[len(parts)-1])
	if ext == "" {
		ext = ".dat"
	}

	if err := d.server.storageMgr.EnsureCameraDir(cameraID); err != nil {
		return nil, fmt.Errorf("ftp: cannot create camera directory: %w", err)
	}

	timestamp := time.Now().UnixMilli()
	autoName := fmt.Sprintf("%s_%d%s", cameraID, timestamp, ext)
	finalPath := filepath.Join(d.server.storageMgr.RootDir(), cameraID, autoName)

	f, err := os.Create(finalPath)
	if err != nil {
		return nil, fmt.Errorf("ftp: cannot create file: %w", err)
	}

	return &uploadFileTransfer{
		File:      f,
		server:    d.server,
		cameraID:  cameraID,
		filePath:  finalPath,
		format:    extToFormat(ext),
		startedAt: time.Now(),
	}, nil
}

// ---- file wrappers ----

// fileWrapper adapts *os.File to satisfy afero.File (adds WriteString).
type fileWrapper struct {
	*os.File
}

func (f *fileWrapper) WriteString(s string) (int, error) {
	return f.File.WriteString(s)
}

// uploadFileTransfer tracks write size and creates a DB recording entry on Close.
type uploadFileTransfer struct {
	*os.File
	server    *Server
	cameraID  string
	filePath  string
	format    model.Format
	startedAt time.Time
	size      int64
}

func (u *uploadFileTransfer) Write(p []byte) (n int, err error) {
	n, err = u.File.Write(p)
	u.size += int64(n)
	return
}

func (u *uploadFileTransfer) Close() error {
	if err := u.File.Close(); err != nil {
		return err
	}

	endedAt := time.Now()
	recording := &model.Recording{
		ID:        uuid.New().String(),
		CameraID:  u.cameraID,
		FilePath:  u.filePath,
		Format:    u.format,
		StartedAt: u.startedAt,
		EndedAt:   endedAt,
		Duration:  endedAt.Sub(u.startedAt).Seconds(),
		FileSize:  u.size,
		Merged:    false,
	}

	if u.server.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := u.server.db.InsertRecording(ctx, recording); err != nil {
			logger.Error("failed to insert recording for uploaded file", "file_path", u.filePath, "error", err)
		}
	}

	return nil
}

// extToFormat maps a file extension to a model.Format.
func extToFormat(ext string) model.Format {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "mp4", "h264", "avi", "mkv", "mov":
		return model.FormatH264
	case "jpg", "jpeg":
		return model.FormatMJPEG
	default:
		return model.Format("unknown")
	}
}