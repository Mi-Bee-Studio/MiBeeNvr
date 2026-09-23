package app

// builders_maintenance.go — buildAppDeps phase 4: cleanup manager, snapshot
// capturer/runner, MQTT client + trigger dispatcher and the FTP server.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ftp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mqtt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/offload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/snapshot"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// buildMaintenanceDeps mutates deps with the maintenance/integration domain
// and returns the three values consumed by the HTTP wiring phase but not
// stored in appDeps: the timelapse source deleter, the shared snapshot
// capturer and the trigger dispatcher.
func buildMaintenanceDeps(deps *appDeps) (timelapseSourceDeleter, *snapshot.Capturer, triggerDispatcherFunc, error) {
	cfg := deps.cfg
	db := deps.db
	store := deps.store
	m := deps.metrics

	// Step 8: Cleanup manager
	cleanupMgr, err := cleanup.NewCleanupManager(db, store, cfg.Cleanup, m)
	if err != nil {
		return timelapseSourceDeleter{}, nil, nil, fmt.Errorf("cleanup: %w", err)
	}
	cleanupMgr.SetEventBus(deps.eventBus)
	// Wire the live yaml camera set so directory-scanning cleanup (orphan /
	// stale-record) skips dirs belonging to cameras that were removed from the
	// config but whose rows/files linger — avoids recurring O(N) stat
	// scans over orphan dirs on slow USB HDD storage. Retention/disk-threshold
	// cleanup intentionally stays DB-driven so recordings of removed cameras
	// still age out via SQL. Mirrors the provider pattern used by the merge
	// coordinators above.
	cleanupMgr.SetActiveCameraProvider(func() []config.CameraConfig { return cfg.Cameras })
	if cfg.Health.Enabled {
		healthRetention, err := time.ParseDuration(cfg.Health.EventsRetention)
		if err != nil {
			slog.Warn("invalid health events_retention, disabling health cleanup", "error", err)
		} else {
			cleanupMgr.SetHealthConfig(true, healthRetention)
		}
	}

	// Wire transcode orphan cleanup into periodic cleanup
	if deps.transcodeMgr != nil {
		dataDir := cfg.Storage.RootDir
		cleanupMgr.SetTranscodeOrphanCleanup(func(ctx context.Context) error {
			return transcoding.CleanOrphanedTranscodes(ctx, dataDir, db)
		})
	}
	// Wire transcode history retention cleanup
	if cfg.Transcoding.HistoryRetention != "" {
		if hr, err := time.ParseDuration(cfg.Transcoding.HistoryRetention); err == nil {
			cleanupMgr.SetTranscodeHistoryRetention(hr)
		}
	}
	// Pace directory-form (MJPEG/timelapse frame-tree) recording deletion so
	// the opt-in delete_recordings_after_merge cleanup cannot saturate the
	// ext4 journal (jbd2) and starve online recording IO (#748).
	cleanupMgr.SetDirectoryDeleteThrottle(200, 200*time.Millisecond)
	// Bill reclaim I/O to the shared background budget (#751) when enabled;
	// the unlink guardrail replaces the fixed time-slice pacing above (#755).
	if deps.ioBudget != nil {
		cleanupMgr.SetIOBudget(deps.ioBudget)
		cleanupMgr.SetUnlinkBudget(deps.unlinkBudget)
	}
	deps.cleanupMgr = cleanupMgr
	deps.archiveDeleter = cleanup.NewArchiveDeleter(db, store)

	// Step 8.5: offload (optional, issue #874 batch 1) — upload merged
	// recordings to S3-compatible object storage. Pure side channel: the
	// recording hot path is untouched; uploads ride the shared I/O budget as
	// the "offload" tenant. Construction only validates config (no dial) —
	// an unreachable endpoint surfaces as retried upload errors, never a
	// startup crash.
	if cfg.Storage.Remote.Enabled {
		rc := cfg.Storage.Remote
		os, err := objectstore.NewS3(objectstore.Config{
			EndpointURL:     rc.EndpointURL,
			Region:          rc.Region,
			Bucket:          rc.Bucket,
			PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
			AccessKeyID:     rc.AccessKeyID,
			SecretAccessKey: rc.SecretAccessKey,
		})
		if err != nil {
			return timelapseSourceDeleter{}, nil, nil, fmt.Errorf("offload store: %w", err)
		}
		deps.offloadStore = os
		routes := make(map[string]offload.CameraRoute, len(rc.CameraOverrides))
		for camID, ov := range rc.CameraOverrides {
			routes[camID] = offload.CameraRoute{Bucket: ov.Bucket, Prefix: ov.Prefix}
		}
		deps.offloadMgr = offload.NewManager(db, offload.Options{
			Store: os,
			NewBucketStore: func(bucket string) (objectstore.Store, error) {
				return objectstore.NewS3(objectstore.Config{
					EndpointURL:     rc.EndpointURL,
					Region:          rc.Region,
					Bucket:          bucket,
					PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
					AccessKeyID:     rc.AccessKeyID,
					SecretAccessKey: rc.SecretAccessKey,
				})
			},
			CameraRoutes:   routes,
			Budget:         deps.ioBudget,
			Prefix:         rc.Prefix,
			ScanInterval:   time.Duration(rc.Upload.ScanIntervalS) * time.Second,
			MinAge:         time.Duration(rc.Upload.MinAgeS) * time.Second,
			Workers:        rc.Upload.MaxConcurrency,
			BacklogLimit:   rc.Upload.BacklogLimit,
			EvictAfterDays: time.Duration(rc.Evict.AfterDays) * 24 * time.Hour,
			OnStatusCounts: func(counts map[string]int) {
				for status, n := range counts {
					m.OffloadOutboxRows.WithLabelValues(status).Set(float64(n))
				}
			},
			OnUploaded: func(bytes int64) {
				m.OffloadUploadedBytesTotal.Add(float64(bytes))
			},
		})
		deps.offloadProxy = offload.NewProxy(os, offload.ProxyOptions{
			NewBucketStore: func(bucket string) (objectstore.Store, error) {
				return objectstore.NewS3(objectstore.Config{
					EndpointURL:     rc.EndpointURL,
					Region:          rc.Region,
					Bucket:          bucket,
					PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
					AccessKeyID:     rc.AccessKeyID,
					SecretAccessKey: rc.SecretAccessKey,
				})
			},
			PresignFor: func(bucket string) (objectstore.Presigner, error) {
				// Presign against the browser-reachable endpoint when
				// configured; otherwise the store's own endpoint. '' (the
				// default bucket) must resolve to the CONFIGURED bucket —
				// NewS3 rejects an empty one.
				if bucket == "" {
					bucket = rc.Bucket
				}
				pc := objectstore.Config{
					Region:          rc.Region,
					Bucket:          bucket,
					PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
					AccessKeyID:     rc.AccessKeyID,
					SecretAccessKey: rc.SecretAccessKey,
					EndpointURL:     rc.EndpointURL,
				}
				if rc.Playback.EndpointURL != "" {
					pc.EndpointURL = rc.Playback.EndpointURL
				}
				return objectstore.NewS3Presigner(pc)
			},
		})
		slog.Info("remote offload enabled",
			"bucket", rc.Bucket, "prefix", rc.Prefix, "workers", rc.Upload.MaxConcurrency,
			"auto_evict_after_days", rc.Evict.AfterDays)
	}

	// Wire the opt-in delete_recordings_after_merge source deleter into the
	// periodic-merge managers. The per-camera enable flags were applied at
	// manager construction; the mechanism is wired here because the cleanup
	// manager is built after them. BatchDeleteRecordingsWithFiles skips
	// recordings being processed by MiBeeVision.
	tlSourceDeleter := timelapseSourceDeleter{cm: cleanupMgr}
	for _, mgr := range deps.periodicMergeManagers {
		mgr.SetSourceRecordingDeleter(tlSourceDeleter)
	}

	// Shared snapshot capturer (#657): FFmpeg-gated hub-IDR decode for
	// H.264/H.265 cameras + device snapshot-URL fallback. Wired into the
	// latest-frame API below and the MQTT snapshot runner (Step 9). Every
	// consumer degrades gracefully when FFmpeg is absent — the decode just
	// fails and callers fall back / answer 404.
	snapCapturer := &snapshot.Capturer{
		Decode:   transcoding.DecodeAUToJPEG,
		Client:   http.DefaultClient,
		Recorder: deps.camMgr,
		Config:   deps.camMgr,
	}

	// Step 9: Optional MQTT client + shared trigger dispatcher. The dispatcher
	// wires record/stop actions to the camera manager (camMgr is built at Step
	// 5.6) and the snapshot action to the capture→persist→event runner (#656).
	// Built unconditionally so the HTTP webhook trigger (#709) works without
	// MQTT — both trigger sources share this ONE dispatcher (identical action
	// semantics by construction).
	deps.snapRunner = &snapshot.Runner{
		Source:  snapCapturer,
		Storage: &snapshot.Persistor{Root: store.RootDir()},
		Bus:     deps.eventBus,
	}
	triggerDispatcher := mqtt.NewActionDispatcher(deps.camMgr, deps.snapRunner)
	if cfg.MQTT.Enabled {
		deps.mqttClient = mqtt.NewClient(cfg.MQTT.Broker, cfg.MQTT.ClientID, cfg.MQTT.Topic, cfg.MQTT.Username, cfg.MQTT.Password, triggerDispatcher)
	}

	// Wire MQTT client into health manager for event publishing
	if deps.healthMgr != nil && deps.mqttClient != nil {
		deps.healthMgr.SetMQTTClient(deps.mqttClient)
	}

	// Opt-in event-bus → MQTT forwarding (`{prefix}/event/<topic>`) so
	// smart-home platforms consume NVR state without REST polling or an
	// SSE bridge. mqtt.status_events must be explicitly enabled.
	if cfg.MQTT.Enabled && cfg.MQTT.StatusEvents {
		deps.mqttStatusPub = mqtt.NewStatusPublisher(deps.eventBus, deps.mqttClient)
	}

	// Step 10: Optional FTP server
	if cfg.FTP.Enabled != nil && *cfg.FTP.Enabled {
		ftpAddr := fmt.Sprintf(":%d", cfg.FTP.Port)
		ftpUser, ftpPass := cfg.Auth.Username, cfg.Auth.Password
		if cfg.FTP.Username != "" && cfg.FTP.Password != "" {
			ftpUser, ftpPass = cfg.FTP.Username, cfg.FTP.Password
		} else {
			slog.Warn("ftp: 未设置独立凭据,回退到管理员账号——FTP 为明文协议,建议配置 ftp.username/ftp.password / dedicated ftp.username/password recommended (FTP is cleartext)")
		}
		deps.ftpServer = ftp.NewServer(ftpAddr, cfg.FTP.PassivePortRange, ftpUser, ftpPass, store, db)
	}

	return tlSourceDeleter, snapCapturer, triggerDispatcher, nil
}
