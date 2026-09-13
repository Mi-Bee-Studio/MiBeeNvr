package storage

// txn_source.go — DB transaction source attribution (#759).
//
// Control-plane write volume was never quantified per origin: every segment
// pays an insert + close/status writes, merges flip rows, AI writeback,
// health scoring, API edits, cleanup and repair deletes all land on the same
// serialized write connection. Deciding which write paths deserve batching
// needs "how many txns/second, from where" — this file tags every
// instrumented write with a source and hands (source, duration) to the
// metrics layer through the existing optional QueryMetrics hook. Pure
// observation: no write path changes behavior.

import (
	"context"
	"sync/atomic"
	"time"
)

// Transaction source vocabulary (#759) — bounded enum, keep stable for
// dashboards:
//
//	recording_insert — per-segment INSERT (recorder close path)
//	recording_close  — recording-row lifecycle updates (duration fix, merged flag)
//	merge_status     — merge engine row transitions (pending/merged/failed…)
//	ai_event         — MiBeeVision writeback (events + ai_status updates)
//	health           — camera health events
//	api_write        — generic API-originated writes (default ambient)
//	cleanup          — retention/threshold deletes (ctx-tagged)
//	repair           — CLI repair deletes (ctx-tagged)
const (
	txnSourceRecordingInsert = "recording_insert"
	txnSourceRecordingClose  = "recording_close"
	txnSourceMergeStatus     = "merge_status"
	txnSourceAIEvent         = "ai_event"
	txnSourceHealth          = "health"
	txnSourceAPIWrite        = "api_write"
)

type txnSourceCtxKey struct{}

// WithTxnSource tags ctx with the transaction source for write calls made
// under it — the caller-attribution channel for shared methods (deletes are
// API, cleanup or repair depending on who issued them).
func WithTxnSource(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, txnSourceCtxKey{}, source)
}

// txnSourceFrom resolves the effective source: the ctx tag wins, else the
// method-level fallback.
func txnSourceFrom(ctx context.Context, fallback string) string {
	if ctx != nil {
		if s, ok := ctx.Value(txnSourceCtxKey{}).(string); ok && s != "" {
			return s
		}
	}
	return fallback
}

// observeTxn records one write transaction: source (ctx-tag override, else
// the method's fallback) + wall duration. ALWAYS tallied in package-level
// atomics (dashboard snapshot, zero allocation); forwarded to the metrics
// layer when wired.
func (d *DB) observeTxn(ctx context.Context, fallbackSource string, start time.Time) {
	source := txnSourceFrom(ctx, fallbackSource)
	elapsed := time.Since(start)
	if i, ok := txnSourceIndex(source); ok {
		txnCounts[i].Add(1)
		txnNanos[i].Add(int64(elapsed))
	}
	if d.queryMetrics != nil {
		d.queryMetrics.ObserveTxn(source, elapsed.Seconds())
	}
}

// Known sources in stable order (dashboard display + array indexing).
type txnSource int

const (
	txnSrcRecordingInsert txnSource = iota
	txnSrcRecordingClose
	txnSrcMergeStatus
	txnSrcAIEvent
	txnSrcHealth
	txnSrcAPIWrite
	txnSrcCleanup
	txnSrcRepair
	numTxnSources
)

var txnSourceNames = [numTxnSources]string{
	txnSourceRecordingInsert, txnSourceRecordingClose, txnSourceMergeStatus,
	txnSourceAIEvent, txnSourceHealth, txnSourceAPIWrite, "cleanup", "repair",
}

var txnSourceIndexMap = func() map[string]int {
	m := make(map[string]int, numTxnSources)
	for i, s := range txnSourceNames {
		m[s] = i
	}
	return m
}()

var (
	txnCounts [numTxnSources]atomic.Int64
	txnNanos  [numTxnSources]atomic.Int64
)

func txnSourceIndex(source string) (int, bool) {
	i, ok := txnSourceIndexMap[source]
	return i, ok
}

// TxnSnapshot returns the cumulative write-transaction count and total time
// per source since process start (#759 dashboard surface).
func TxnSnapshot() (counts map[string]int64, nanos map[string]int64) {
	counts = make(map[string]int64, numTxnSources)
	nanos = make(map[string]int64, numTxnSources)
	for i, s := range txnSourceNames {
		counts[s] = txnCounts[i].Load()
		nanos[s] = txnNanos[i].Load()
	}
	return counts, nanos
}
