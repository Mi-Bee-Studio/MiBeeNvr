package transcoding

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// DBTaskLister abstracts the database operations needed for orphan cleanup.
type DBTaskLister interface {
	ListTranscodeTasks(ctx context.Context, f storage.TranscodeTaskFilter) ([]storage.TranscodeTask, int, error)
}

// CleanOrphanedTranscodes walks dataDir for files matching *.transcoded.mp4 and
// deletes any that do not have a corresponding task in the database.
// This handles crash recovery: orphaned output files left from tasks that were
// never recorded in DB (e.g., process died mid-enqueue) or whose tasks were
// cleaned up by DeleteCompletedTasks.
func CleanOrphanedTranscodes(ctx context.Context, dataDir string, db DBTaskLister) error {
	// Build set of all known output paths from DB
	tasks, _, err := db.ListTranscodeTasks(ctx, storage.TranscodeTaskFilter{Limit: 200})
	if err != nil {
		return fmt.Errorf("list transcode tasks: %w", err)
	}
	activePaths := make(map[string]struct{}, len(tasks))
	for _, t := range tasks {
		if t.OutputPath != "" {
			activePaths[t.OutputPath] = struct{}{}
		}
	}

	// Walk dataDir for orphaned .transcoded.mp4 files
	var deleted int
	err = filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".transcoded.mp4") {
			return nil
		}

		// Check if context cancelled
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, ok := activePaths[path]; ok {
			return nil // has active task, keep it
		}

		// Orphaned — delete it
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to delete orphaned transcoded file", "path", path, "error", err)
			return nil // non-fatal, continue walking
		}
		slog.Info("deleted orphaned transcoded file", "path", path)
		deleted++
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk data dir: %w", err)
	}

	if deleted > 0 {
		slog.Info("cleaned up orphaned transcoded files", "count", deleted)
	}
	return nil
}
