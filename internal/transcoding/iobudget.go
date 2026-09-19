package transcoding

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// ioBudget paces transcode I/O against the process-wide background budget
// (#848). nil = budgeting off (the io.budget_bytes_per_sec default) — tasks
// run unthrottled exactly as before this variable existed. Set once by
// pkg/app wiring at startup, before the queue starts; read-only afterwards.
var ioBudget iobudget.Limiter

// SetIOBudget installs the shared background I/O budget for all transcode
// work (input read + output write estimates). Passing nil disables pacing.
func SetIOBudget(l iobudget.Limiter) { ioBudget = l }

// waitTranscodeIOBudget bills n bytes of transcode I/O. With no budget
// installed it is a nil check. It can only fail with the caller's own ctx
// error (shutdown or job timeout) — the single call site aborts the task as
// cancelled, so pacing adds no new failure semantics.
func waitTranscodeIOBudget(ctx context.Context, n int64) error {
	if ioBudget == nil {
		return nil
	}
	return ioBudget.Wait(ctx, iobudget.ConsumerTranscode, n)
}
