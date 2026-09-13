package timelapse

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// ioBudget paces timelapse frame extraction against the process-wide
// background budget (#751). nil = budgeting off (default) — extraction runs
// unthrottled exactly as before. Set once by pkg/app wiring at startup.
var ioBudget iobudget.Limiter

// SetIOBudget installs the shared background I/O budget for frame extraction
// (AVI chunk reads, MP4 sync-sample reads, frame file writes). Passing nil
// disables pacing.
func SetIOBudget(l iobudget.Limiter) { ioBudget = l }

// waitIOBudget bills n bytes of extraction I/O. It can only fail with the
// caller's own ctx error; periodic merges already abort on ctx.Err(), so
// pacing adds no new failure semantics.
func waitIOBudget(ctx context.Context, n int64) error {
	if ioBudget == nil {
		return nil
	}
	return ioBudget.Wait(ctx, iobudget.ConsumerTimelapse, n)
}
