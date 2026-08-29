package sip

// GB28181 sub-channel probing + sub-stream pull sessions (#560).
//
// Vendor-convention sub channels (Hikvision: channel code +1) are not listed
// in the device catalog — the only way to discover them is to INVITE the
// derived code and see whether media arrives. The prober does exactly that,
// once per main channel per process lifetime, silently: a camera whose device
// has no usable sub channel keeps the plain no-sub-stream degradation path
// and never shows an error state.
//
// The discovered code is persisted on the camera (GB28181.SubChannelID,
// fill-once) and consumed by the on-demand sub-stream puller via
// InviteSubChannel, which feeds the demuxed AUs into the camera's sub hub.

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mickeyzzc/gb28181-go/platform"
)

// subProbeWait delays probing after a catalog merge so the main channels'
// auto-INVITE sessions settle first — single-stream firmwares would otherwise
// see the probe INVITE race the main INVITE. Atomic nanosecond counters (not
// plain vars) so tests can shrink them without racing probe goroutines.
var (
	subProbeWait    atomic.Int64
	subProbeTimeout atomic.Int64
)

func init() {
	subProbeWait.Store(int64(5 * time.Second))
	subProbeTimeout.Store(int64(6 * time.Second))
}

// subProbed memoizes probed main channels for the process lifetime (success
// AND failure — the probe never repeats within one boot; clearing the
// persisted sub_channel_id and restarting re-arms it).
var subProbed sync.Map // "deviceID/channelID" -> struct{}

// EnsureSubChannelRegistered registers a probe-discovered sub-channel as a
// synthetic pull target when the device's catalog does not advertise it.
// Catalog refreshes never list the code, so the SubProbe flag distinguishes
// it from real channels (nothing enrolls a camera for it). Idempotent.
func (s *Server) EnsureSubChannelRegistered(deviceID, channelID string) error {
	if _, ok := s.deviceMgr.FindChannel(deviceID, channelID); ok {
		return nil
	}
	if _, ok := s.deviceMgr.Device(deviceID); !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	s.deviceMgr.RegisterChannel(deviceID, &platform.Channel{
		DeviceID: deviceID,
		ID:       channelID,
		Name:     "sub " + channelID,
		SubProbe: true,
	})
	return nil
}

// InviteSubChannel establishes a media session for a probed sub-channel whose
// demuxed video AUs feed the on-demand sub-stream puller (NOT a recorder).
// The returned release func BYEs the session and drops the dialog. Stall
// handling is the caller's: the substream manager reconnects on its own
// timers, so the recorder-oriented watchSession is deliberately NOT armed
// (it would re-INVITE with recorder callbacks this channel does not have).
func (s *Server) InviteSubChannel(deviceID, channelID string, onAU func(au [][]byte, ptsTicks int64, isIDR bool)) (func(), error) {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return nil, fmt.Errorf("gb28181: SIP server not started")
	}

	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok {
		return nil, fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	ch, ok := s.deviceMgr.FindChannel(deviceID, channelID)
	if !ok {
		return nil, fmt.Errorf("gb28181: sub-channel %q not registered on device %q", channelID, deviceID)
	}

	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	serverHost := s.localIPFor(netAddr)

	if err := s.inviteCore(deviceID, ch, netAddr, serverHost, onAU, nil); err != nil {
		return nil, err
	}
	_ = s.sessionMgr.MarkPlaying(channelID)

	return func() {
		s.mu.Lock()
		delete(s.dialogs, channelID)
		s.mu.Unlock()
		_ = s.sessionMgr.Bye(channelID)
	}, nil
}

// maybeProbeSubChannels runs the prober for one device's channels after its
// catalog merged (and on later refreshes — the per-channel memo makes those
// no-ops). Cameras that already carry a persisted sub_channel_id only get
// their synthetic channel re-registered (NVR restart wiped the in-memory
// channel table; the catalog never lists it).
func (s *Server) maybeProbeSubChannels(deviceID string) {
	mode := s.cfg.SubChannelProbe
	offset := s.cfg.SubChannelProbeOffset
	if mode == "off" || offset <= 0 {
		return
	}

	enrol := s.enroller()
	if enrol == nil {
		return
	}

	for _, ch := range s.deviceMgr.Channels(deviceID) {
		if ch.SubProbe {
			continue
		}
		mainCh := ch
		go func() {
			time.Sleep(time.Duration(subProbeWait.Load())) // let the main INVITE settle first
			// BOTH gates resolve INSIDE the delayed goroutine — at merge time
			// the catalog-driven camera enrollment (EnsureGB28181Camera) is
			// typically still in flight, and the DeviceInfo response carrying
			// the manufacturer races the catalog response to the same
			// millisecond. Resolving either at trigger time skipped every
			// freshly-registered device.
			if mode != "on" {
				mfr := ""
				if dev, ok := s.deviceMgr.Device(deviceID); ok {
					dev.Mu.RLock()
					mfr = dev.Manufacturer
					dev.Mu.RUnlock()
				}
				if !knownOffsetVendor(mfr) {
					return
				}
			}
			if _, ok := enrol.GB28181CameraIDByChannel(deviceID, mainCh.ID); !ok {
				return // no camera bound — nothing to attach a sub stream to
			}
			s.probeSubChannel(deviceID, mainCh, offset)
		}()
	}
}

// probeSubChannel probes ONE main channel's vendor-convention sub candidate:
// derive the code, INVITE it with a throwaway frame signal, and persist on
// first media. Every failure path is silent (Debug) — incapable devices must
// show zero error state (#560 acceptance).
func (s *Server) probeSubChannel(deviceID string, ch *platform.Channel, offset int) {
	enrol := s.enroller()
	if enrol == nil {
		return
	}

	// A persisted sub_channel_id means a prior probe (or manual config)
	// already resolved this camera's sub — just make sure the synthetic
	// channel is registered for the puller, then stop. Fill-once: an existing
	// value is never re-probed.
	if sub := enrol.GB28181SubChannelID(deviceID, ch.ID); sub != "" {
		if err := s.EnsureSubChannelRegistered(deviceID, sub); err != nil {
			slog.Debug("gb28181: re-register sub-channel failed", "device", deviceID, "sub_channel", sub, "error", err)
		}
		return
	}

	probeKey := deviceID + "/" + ch.ID
	if _, done := subProbed.LoadOrStore(probeKey, struct{}{}); done {
		return
	}

	candidate := offsetChannelCode(ch.ID, offset)
	if candidate == "" {
		return
	}
	// Never repurpose an advertised channel: on multi-channel devices (NVRs)
	// the +1 code is a REAL sibling channel with its own camera. Only codes
	// the catalog does not list qualify as sub candidates.
	if _, exists := s.deviceMgr.FindChannel(deviceID, candidate); exists {
		return
	}

	if err := s.EnsureSubChannelRegistered(deviceID, candidate); err != nil {
		slog.Debug("gb28181: sub-channel register failed", "device", deviceID, "candidate", candidate, "error", err)
		return
	}

	gotFrame := make(chan struct{}, 1)
	onAU := func(au [][]byte, ptsTicks int64, isIDR bool) {
		select {
		case gotFrame <- struct{}{}:
		default:
		}
	}
	release, err := s.InviteSubChannel(deviceID, candidate, onAU)
	if err != nil {
		slog.Debug("gb28181: sub-channel probe INVITE failed (silent — no sub stream)",
			"device", deviceID, "channel", ch.ID, "candidate", candidate, "error", err)
		s.deviceMgr.UnregisterChannel(deviceID, candidate)
		return
	}

	select {
	case <-gotFrame:
		release()
		if err := enrol.SetGB28181SubChannel(deviceID, ch.ID, candidate); err != nil {
			slog.Warn("gb28181: persist sub_channel_id failed", "device", deviceID, "channel", ch.ID, "error", err)
			s.deviceMgr.UnregisterChannel(deviceID, candidate)
			return
		}
		cameraID, _ := enrol.GB28181CameraIDByChannel(deviceID, ch.ID)
		slog.Info("gb28181: sub-channel probed — sub stream available",
			"device", deviceID, "channel", ch.ID, "sub_channel", candidate, "camera", cameraID)
	case <-time.After(time.Duration(subProbeTimeout.Load())):
		release()
		s.deviceMgr.UnregisterChannel(deviceID, candidate)
		slog.Debug("gb28181: sub-channel probe timed out (silent — no sub stream)",
			"device", deviceID, "channel", ch.ID, "candidate", candidate)
	}
}

// offsetChannelCode adds offset to a GB 20-digit decimal channel code,
// carrying from the last digit, preserving the length (a carry past the first
// digit, or any non-digit code, yields "" — not a usable candidate).
func offsetChannelCode(id string, offset int) string {
	if offset <= 0 || len(id) == 0 {
		return ""
	}
	digits := []byte(id)
	for _, d := range digits {
		if d < '0' || d > '9' {
			return ""
		}
	}
	for i := len(digits) - 1; i >= 0 && offset > 0; i-- {
		v := int(digits[i]-'0') + offset
		digits[i] = byte('0' + v%10)
		offset = v / 10
	}
	if offset > 0 {
		return "" // overflow past the most significant digit
	}
	return string(digits)
}

// knownOffsetVendor reports whether the DeviceInfo manufacturer is known to
// follow the channel-code offset convention (probe gate for "auto" mode).
func knownOffsetVendor(manufacturer string) bool {
	m := strings.ToLower(manufacturer)
	return strings.Contains(m, "hikvision") || strings.Contains(m, "海康") ||
		strings.Contains(m, "dahua") || strings.Contains(m, "大华")
}
