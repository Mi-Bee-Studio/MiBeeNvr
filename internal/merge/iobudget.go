package merge

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// ioBudget paces merge I/O against the process-wide background budget
// (#751). nil = budgeting off (the default) — merges run unthrottled exactly
// as before this variable existed. Set once by pkg/app wiring at startup,
// before the first merge runs; read-only afterwards.
var ioBudget iobudget.Limiter

// SetIOBudget installs the shared background I/O budget for all merge work
// (MP4 sample streaming, AVI chunk streaming). Passing nil disables pacing.
func SetIOBudget(l iobudget.Limiter) { ioBudget = l }

// waitIOBudget bills n bytes of merge I/O. With no budget installed it is a
// nil check. It can only fail with the caller's own ctx error — every call
// site already aborts on ctx.Err(), so pacing adds no new failure semantics.
func waitIOBudget(ctx context.Context, n int64) error {
	if ioBudget == nil {
		return nil
	}
	return ioBudget.Wait(ctx, iobudget.ConsumerMerge, n)
}
