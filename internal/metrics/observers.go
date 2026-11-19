package metrics

// Observation methods on Metrics: the per-subsystem counters/histograms that
// recording, merging, playback, p2p, storage and SQLite call into. Split out
// of metrics.go, which keeps the field declarations and registration.

import "time"

// ObserveQueryDuration implements storage.QueryMetrics, recording a query latency into
// the nvr_sqlite_query_duration_seconds histogram. Called from hot DB methods.
// ObserveTxn implements storage.QueryMetrics (#759): one write transaction
// by source. Bounded enum on the source label — see storage/txn_source.go.
func (m *Metrics) ObserveTxn(source string, seconds float64) {
	if m == nil || m.SQLiteTxnsTotal == nil {
		return
	}
	m.SQLiteTxnsTotal.WithLabelValues(source).Inc()
	m.SQLiteTxnDurationSeconds.WithLabelValues(source).Observe(seconds)
}

func (m *Metrics) ObserveQueryDuration(queryName string, seconds float64) {
	if m == nil || m.SQLiteQueryDurationSeconds == nil {
		return
	}
	m.SQLiteQueryDurationSeconds.WithLabelValues(queryName).Observe(seconds)
}

// IncSQLiteBusyErrors implements storage.QueryMetrics, incrementing the
// nvr_sqlite_busy_errors_total counter. Called from RetryOnBusy on each SQLITE_BUSY retry.
func (m *Metrics) IncSQLiteBusyErrors() {
	if m == nil || m.SQLiteBusyErrorsTotal == nil {
		return
	}
	m.SQLiteBusyErrorsTotal.Inc()
}

// SetP2PICESessions sets the current P2P WebRTC session count for a nominated
// local candidate type (count 0 removes the series). Nil-safe — p2p telemetry
// must never break session establishment (#12 acceptance).
func (m *Metrics) SetP2PICESessions(localType string, count int) {
	if m == nil || m.P2PICESessions == nil {
		return
	}
	if count <= 0 {
		m.P2PICESessions.DeleteLabelValues(localType)
		return
	}
	m.P2PICESessions.WithLabelValues(localType).Set(float64(count))
}

// ObserveP2PICERTT records a nominated ICE pair round-trip in milliseconds.
// Nil-safe; non-positive RTT (no nominated pair yet) is skipped.
func (m *Metrics) ObserveP2PICERTT(localType string, milliseconds float64) {
	if m == nil || m.P2PICERTTMillis == nil || milliseconds <= 0 {
		return
	}
	m.P2PICERTTMillis.WithLabelValues(localType).Observe(milliseconds)
}

// RecordMergeSuccess records a successful merge operation.
func (m *Metrics) RecordMergeSuccess(duration time.Duration, size int64) {
	if m == nil {
		return
	}
	m.MergeAttemptsTotal.Inc()
	m.MergeSuccessesTotal.Inc()
	m.MergeDurationSeconds.Observe(duration.Seconds())
	m.MergeSizeBytes.Observe(float64(size))
}

// RecordMergeFailure records a failed merge operation with the given reason.
func (m *Metrics) RecordMergeFailure(reason string) {
	if m == nil {
		return
	}
	m.MergeAttemptsTotal.Inc()
	m.MergeFailuresTotal.WithLabelValues(reason).Inc()
}

// UpdateMergePending updates the pending segment count gauge for a camera.
func (m *Metrics) UpdateMergePending(cameraID string, count float64) {
	if m == nil {
		return
	}
	m.MergePendingSegments.WithLabelValues(cameraID).Set(count)
}

// RecordRollingMergeLatency records the end-to-end latency of a rolling merge
// (segment close → merge complete) for a camera.
func (m *Metrics) RecordRollingMergeLatency(cameraID string, latency time.Duration) {
	if m == nil || m.RollingMergeLatencySeconds == nil {
		return
	}
	m.RollingMergeLatencySeconds.WithLabelValues(cameraID).Observe(latency.Seconds())
}

// UpdateRollingMergeBucketSegments sets the current segment count in a camera's
// active rolling merge window bucket.
func (m *Metrics) UpdateRollingMergeBucketSegments(cameraID string, count int) {
	if m == nil || m.RollingMergeBucketSegments == nil {
		return
	}
	m.RollingMergeBucketSegments.WithLabelValues(cameraID).Set(float64(count))
}

// RecordPixgateSample records one processed pixel-gate sample: the counter
// carries camera+source, the gauges refresh the sampler heartbeat (unix
// timestamp) and the last FG area (#699).
func (m *Metrics) RecordPixgateSample(cameraID, source string, areaPct float64) {
	if m == nil || m.PixgateSamplesTotal == nil {
		return
	}
	m.PixgateSamplesTotal.WithLabelValues(cameraID, source).Inc()
	if m.PixgateLastAreaPct != nil {
		m.PixgateLastAreaPct.WithLabelValues(cameraID).Set(areaPct)
	}
	if m.PixgateLastSampleTimestamp != nil {
		m.PixgateLastSampleTimestamp.WithLabelValues(cameraID).Set(float64(time.Now().UnixNano()) / 1e9)
	}
}

// RecordPixgateTrigger records a confirmed-activity trigger (full-rate exit)
// for a camera (#699).
func (m *Metrics) RecordPixgateTrigger(cameraID string) {
	if m == nil || m.PixgateTriggersTotal == nil {
		return
	}
	m.PixgateTriggersTotal.WithLabelValues(cameraID).Inc()
}

// RecordRollingBucketFinalized records a rolling bucket leaving the retained
// set (#764) — reason is a bounded enum (idle_ttl/capacity_lru/size_limit),
// lifetime is the wall time from bucket creation to finalize.
func (m *Metrics) RecordRollingBucketFinalized(reason string, lifetime time.Duration) {
	if m == nil || m.RollingBucketFinalizedTotal == nil {
		return
	}
	m.RollingBucketFinalizedTotal.WithLabelValues(reason).Inc()
	if m.RollingBucketLifetimeSeconds != nil {
		m.RollingBucketLifetimeSeconds.WithLabelValues(reason).Observe(lifetime.Seconds())
	}
}

// IncStorageWriteErrors increments the storage write errors counter.
func (m *Metrics) IncStorageWriteErrors() {
	if m == nil {
		return
	}
	m.StorageWriteErrors.Inc()
}

// SetPlaybackLiveLatency records a player-reported end-to-end live latency
// (ms) for a camera+protocol, aggregated from /api/telemetry beacons.
func (m *Metrics) SetPlaybackLiveLatency(cameraID, protocol string, latencyMS float64) {
	if m == nil || m.PlaybackLiveLatencyMS == nil {
		return
	}
	m.PlaybackLiveLatencyMS.WithLabelValues(cameraID, protocol).Set(latencyMS)
}

// IncPlaybackStall records a player-reported playback stall (buffering/freeze).
func (m *Metrics) IncPlaybackStall(cameraID, protocol string) {
	if m == nil || m.PlaybackStallsTotal == nil {
		return
	}
	m.PlaybackStallsTotal.WithLabelValues(cameraID, protocol).Inc()
}

// ObserveSegmentWrite records the wall time to persist one MP4 segment —
// sustained growth is the SD-card/USB degradation early-warning signal.
func (m *Metrics) ObserveSegmentWrite(cameraID string, d time.Duration) {
	if m == nil || m.SegmentWriteDurationSeconds == nil {
		return
	}
	m.SegmentWriteDurationSeconds.WithLabelValues(cameraID).Observe(d.Seconds())
}

// IncRecordingAudit records one recording-integrity audit outcome
// (ok | zero_duration | probe_error).
func (m *Metrics) IncRecordingAudit(cameraID, result string) {
	if m == nil || m.RecordingAuditTotal == nil {
		return
	}
	m.RecordingAuditTotal.WithLabelValues(cameraID, result).Inc()
}

// IncRecordingDeepCheck records one decode-level deep check outcome
// (ok | decode_error) — ffmpeg sampling per #489.
func (m *Metrics) IncRecordingDeepCheck(cameraID, result string) {
	if m == nil || m.RecordingDeepCheckTotal == nil {
		return
	}
	m.RecordingDeepCheckTotal.WithLabelValues(cameraID, result).Inc()
}

// SetP2PTunnelDroppedFrames publishes the cumulative tunnel drop counter
// (nil-safe; negative values are ignored).
func (m *Metrics) SetP2PTunnelDroppedFrames(total uint64) {
	if m == nil || m.P2PTunnelDroppedFrames == nil {
		return
	}
	m.P2PTunnelDroppedFrames.Set(float64(total))
}

// SetP2PNATDualStack publishes whether the machine carries a global-unicast
// IPv6 address (nil-safe).
func (m *Metrics) SetP2PNATDualStack(present bool) {
	if m == nil || m.P2PNATDualStack == nil {
		return
	}
	if present {
		m.P2PNATDualStack.Set(1)
	} else {
		m.P2PNATDualStack.Set(0)
	}
}

// IncP2PRelayDial counts one relay connect attempt (nil-safe).
func (m *Metrics) IncP2PRelayDial() {
	if m == nil || m.P2PRelayDials == nil {
		return
	}
	m.P2PRelayDials.Inc()
}

// IncP2PRelayDialFailure counts one failed relay connect attempt by reason
// (overloaded|auth|rate_limited|network; nil-safe).
func (m *Metrics) IncP2PRelayDialFailure(reason string) {
	if m == nil || m.P2PRelayDialFailures == nil {
		return
	}
	m.P2PRelayDialFailures.WithLabelValues(reason).Inc()
}

// SetP2PRelayCircuitState publishes the relay circuit breaker state as the
// numeric gauge value (nil-safe).
func (m *Metrics) SetP2PRelayCircuitState(v float64) {
	if m == nil || m.P2PRelayCircuitState == nil {
		return
	}
	m.P2PRelayCircuitState.Set(v)
}

// SetP2PNATClass publishes the observed NAT class — 1 on the current class,
// deleting other series so exactly one label is live (nil-safe).
func (m *Metrics) SetP2PNATClass(class string) {
	if m == nil || m.P2PNATClass == nil {
		return
	}
	for _, c := range []string{"public", "cone", "symmetric", "unknown"} {
		if c == class {
			m.P2PNATClass.WithLabelValues(c).Set(1)
		} else {
			m.P2PNATClass.DeleteLabelValues(c)
		}
	}
}
