package gb28181

import (
	"context"
	"sync"

	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// This file is the host-side assembly layer over gb28181-go's platform/cascade
// client (batch 4 of the library migration): config mapping, the Store seam
// over storage.DB, the SegmentParser seam over internal/merge, and the
// streamhub→FrameHub forwarding bridge. The cascade protocol logic itself
// lives in the library.

// CascadeConfig maps the NVR YAML config onto the library's cascade.Config.
// The library struct is a documented superset (DeviceName/Manufacturer/Model
// fall back to the MiBee NVR identity when zero).
func CascadeConfig(cfg config.GB28181CascadeConfig) gbcascade.Config {
	out := gbcascade.Config{
		Enabled:           cfg.Enabled,
		ServerDomain:      cfg.ServerDomain,
		ServerAddr:        cfg.ServerAddr,
		LocalDeviceID:     cfg.LocalDeviceID,
		Realm:             cfg.Realm,
		Password:          cfg.Password,
		SIPListen:         cfg.SIPListen,
		HeartbeatInterval: cfg.HeartbeatInterval,
		RegisterExpires:   cfg.RegisterExpires,
	}
	for _, u := range cfg.Upstreams {
		out.Upstreams = append(out.Upstreams, gbcascade.Upstream{
			ServerDomain:      u.ServerDomain,
			ServerAddr:        u.ServerAddr,
			LocalDeviceID:     u.LocalDeviceID,
			Realm:             u.Realm,
			Password:          u.Password,
			HeartbeatInterval: u.HeartbeatInterval,
			RegisterExpires:   u.RegisterExpires,
		})
	}
	return out
}

// NewCascadeStore adapts storage.DB to the library's cascade Store seam.
func NewCascadeStore(db *storage.DB) gbcascade.Store {
	if db == nil {
		return nil
	}
	return &cascadeStoreAdapter{db: db}
}

type cascadeStoreAdapter struct {
	db *storage.DB
}

func (a *cascadeStoreAdapter) UpsertCascadeChannel(ctx context.Context, ch gbcascade.CascadeChannel) error {
	return a.db.UpsertCascadeChannel(ctx, storage.CascadeChannel{
		CameraID:    ch.CameraID,
		GBChannelID: ch.GBChannelID,
		Name:        ch.Name,
		UpdatedAt:   ch.UpdatedAt,
	})
}

func (a *cascadeStoreAdapter) ListCascadeChannels(ctx context.Context) ([]gbcascade.CascadeChannel, error) {
	rows, err := a.db.ListCascadeChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gbcascade.CascadeChannel, len(rows))
	for i, r := range rows {
		out[i] = gbcascade.CascadeChannel{
			CameraID:    r.CameraID,
			GBChannelID: r.GBChannelID,
			Name:        r.Name,
			UpdatedAt:   r.UpdatedAt,
		}
	}
	return out, nil
}

func (a *cascadeStoreAdapter) ListRecordings(ctx context.Context, filter gbcascade.RecordingFilter) ([]gbcascade.Recording, error) {
	rows, err := a.db.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  filter.CameraID,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
		Limit:     filter.Limit,
		SortBy:    filter.SortBy,
		SortOrder: filter.SortOrder,
	})
	if err != nil {
		return nil, err
	}
	out := make([]gbcascade.Recording, len(rows))
	for i, r := range rows {
		out[i] = gbcascade.Recording{
			ID:        r.ID,
			CameraID:  r.CameraID,
			FilePath:  r.FilePath,
			Format:    gbcascade.Format(r.Format),
			StartedAt: r.StartedAt,
			EndedAt:   r.EndedAt,
			Duration:  r.Duration,
		}
	}
	return out, nil
}

// SegmentParser adapts internal/merge.ParseSegment to the library's
// SegmentParser seam — a type-for-type wrapper (the lib SegmentInfo/Sample
// field names were taken from merge's).
func SegmentParser() gbcascade.SegmentParser {
	return func(filePath string) (*gbcascade.SegmentInfo, error) {
		info, err := merge.ParseSegment(filePath)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, nil
		}
		out := &gbcascade.SegmentInfo{
			Codec:     info.Codec,
			SPS:       info.SPS,
			PPS:       info.PPS,
			VPS:       info.VPS,
			Timescale: info.Timescale,
			Samples:   make([]gbcascade.SegmentSample, len(info.Samples)),
		}
		for i, s := range info.Samples {
			out.Samples[i] = gbcascade.SegmentSample{
				Offset:     s.Offset,
				Size:       s.Size,
				Duration:   s.Duration,
				IsKeyFrame: s.IsKeyFrame,
			}
		}
		return out, nil
	}
}

// --- streamhub → FrameHub forwarding bridge ---

var (
	bridgeMu    sync.Mutex
	bridgeCache = map[*streamhub.StreamHub]*platform.FrameHub{}
)

// bridgeSubscribe wires one forwarding consumer (video + audio) from an NVR
// stream hub into a fresh library FrameHub and returns a detach that
// unsubscribes both.
func bridgeSubscribe(nvr *streamhub.StreamHub, subID string) (*platform.FrameHub, func()) {
	lib := platform.NewFrameHub()
	_ = nvr.Subscribe(subID, func(pts int64, au [][]byte) {
		// isIDR stays false: FrameHub consumers never read it (the lib keeps
		// the parameter for host-side adapters that need it — we don't).
		lib.Broadcast(pts, au, false)
	})
	_ = nvr.SubscribeAudio(subID, func(pts int64, codec model.AudioCodec, data []byte) {
		lib.BroadcastAudio(pts, string(codec), data)
	})
	return lib, func() {
		nvr.Unsubscribe(subID)
		nvr.UnsubscribeAudio(subID)
	}
}

// BridgeHub returns a process-lifetime FrameHub mirror of an NVR stream hub.
// The first call for a hub subscribes the forwarding consumer; later calls
// reuse it (cascade sessions come and go per INVITE, the bridge stays).
// Frames keep flowing while no cascade session is active — one extra
// callback per frame on cameras the cascade has INVITEd at least once, and
// FrameHub.Broadcast with zero consumers is a no-op.
func BridgeHub(nvr *streamhub.StreamHub) *platform.FrameHub {
	if nvr == nil {
		return nil
	}
	bridgeMu.Lock()
	defer bridgeMu.Unlock()
	if lib, ok := bridgeCache[nvr]; ok {
		return lib
	}
	lib, _ := bridgeSubscribe(nvr, "cascade-bridge")
	bridgeCache[nvr] = lib
	return lib
}

// BridgeSubHub wraps a short-lived hub (an on-demand sub-stream): the
// returned detach unsubscribes the forwarding consumer, so the release path
// composes it with the sub-stream's own release.
func BridgeSubHub(nvr *streamhub.StreamHub, subID string) (*platform.FrameHub, func()) {
	if nvr == nil {
		return nil, func() {}
	}
	return bridgeSubscribe(nvr, subID)
}
