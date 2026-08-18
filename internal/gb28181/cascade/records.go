package cascade

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// recordPageSize bounds the RecordItems carried by one SIP MESSAGE. SIP over
// UDP fragments above ~64KB and many platforms cap MESSAGE bodies far lower;
// paginating with a stable SN lets the upper platform fold pages via its
// pending-record-query accumulator.
const recordPageSize = 40

// gbTimeLayout is the naive GB/T 28181 timestamp form (device-local wall
// clock, no zone). Queries arrive in the upper platform's naive clock; the
// cascade answers in its own — both sides must agree on the zone, which
// SetGBTimezone pins when the hosts' system zones diverge.
const gbTimeLayout = "2006-01-02T15:04:05"

// answerRecordInfo replies to the upper platform's recording query with the
// local camera's recorded segments in the requested window. The response
// echoes the queried channel ID (platforms correlate on DeviceID+SN — some
// echo the device ID, but the channel form is what our own platform keys on).
func (s *Service) answerRecordInfo(u *upper, q manscdp.RecordInfoQuery) {
	cameraID, ok := s.cameraOfChannel(q.DeviceID)
	if !ok {
		slog.Warn("gb28181-cascade: RecordInfo for unknown channel", "channel", q.DeviceID)
		return
	}
	start, err1 := time.ParseInLocation(gbTimeLayout, q.StartTime, s.gbTZ())
	end, err2 := time.ParseInLocation(gbTimeLayout, q.EndTime, s.gbTZ())
	if err1 != nil || err2 != nil || !end.After(start) {
		slog.Warn("gb28181-cascade: RecordInfo query with bad time range",
			"channel", q.DeviceID, "start", q.StartTime, "end", q.EndTime)
		return
	}
	if end.Sub(start) > 31*24*time.Hour {
		end = start.Add(31 * 24 * time.Hour) // bound the DB scan
	}

	recs, err := s.db.ListRecordings(context.Background(), model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: start,
		EndTime:   end,
		Limit:     2000,
		SortBy:    "started_at",
		SortOrder: "asc",
	})
	if err != nil {
		slog.Warn("gb28181-cascade: recordings query failed", "camera", cameraID, "error", err)
		return
	}

	name := q.DeviceID
	if cam, ok := s.cameraInfo(cameraID); ok && cam.Name != "" {
		name = cam.Name
	}
	items := make([]manscdp.RecordItem, 0, len(recs))
	for _, rec := range recs {
		items = append(items, manscdp.RecordItem{
			DeviceID:  q.DeviceID,
			Name:      name,
			StartTime: rec.StartedAt.In(s.gbTZ()).Format(gbTimeLayout),
			EndTime:   rec.EndedAt.In(s.gbTZ()).Format(gbTimeLayout),
			Secrecy:   0,
			Type:      "time",
		})
	}

	// First page (or only page) always carries SumNum so the platform can
	// detect completion even when later pages are lost.
	for off := 0; off < len(items) || off == 0; off += recordPageSize {
		page := items[off:min(off+recordPageSize, len(items))]
		body, err := manscdp.Encode(manscdp.RecordInfo{
			CmdType:    manscdp.CmdRecordInfo,
			SN:         q.SN,
			DeviceID:   q.DeviceID,
			Name:       name,
			SumNum:     len(items),
			RecordList: page,
		})
		if err != nil {
			return
		}
		if err := s.sendMessageBodyTo(u, body, "Application/MANSCDP+xml"); err != nil {
			slog.Warn("gb28181-cascade: record info page failed",
				"channel", q.DeviceID, "page", off/recordPageSize, "error", err)
			return
		}
	}
	slog.Info("gb28181-cascade: record info answered",
		"channel", q.DeviceID, "camera", cameraID, "records", len(items))
}

// PTZForwarder translates a decoded GB/T 28181 PTZ direction into the local
// camera's native PTZ control (ONVIF continuous move, Xiaomi motor, or the
// local GB28181 platform). Set at wiring time; nil disables forwarding.
type PTZForwarder func(cameraID, direction string, speed byte) error

// SetPTZForwarder wires the local-camera PTZ bridge.
func (s *Service) SetPTZForwarder(f PTZForwarder) {
	s.ptzForward = f
}

// forwardDeviceControl routes an upper-platform DeviceControl to the local
// camera behind the channel. PTZ commands forward to the camera's own PTZ
// path; the management instructions (RecordCmd/GuardCmd/AlarmCmd/TeleBoot/
// HomePosition) have no local equivalent on an NVR lower — they are answered
// with an explicit unsupported log instead of silent drops so operators can
// see the upper platform's intent (#379). TeleBoot is additionally refused
// outright: an upper platform must not be able to power-cycle this host.
func (s *Service) forwardDeviceControl(dc manscdp.DeviceControl) {
	if dc.PTZCmd == "" {
		switch {
		case dc.RecordCmd != "":
			slog.Warn("gb28181-cascade: RecordCmd has no local equivalent — ignored",
				"channel", dc.DeviceID, "cmd", dc.RecordCmd)
		case dc.GuardCmd != "":
			slog.Warn("gb28181-cascade: GuardCmd has no local equivalent — ignored",
				"channel", dc.DeviceID, "cmd", dc.GuardCmd)
		case dc.AlarmCmd != "":
			slog.Warn("gb28181-cascade: AlarmCmd has no local equivalent — ignored",
				"channel", dc.DeviceID, "cmd", dc.AlarmCmd)
		case dc.TeleBoot != "":
			slog.Warn("gb28181-cascade: TeleBoot refused (will not reboot this host)",
				"channel", dc.DeviceID)
		case dc.HomePosition != "":
			slog.Warn("gb28181-cascade: HomePosition has no local equivalent — ignored",
				"channel", dc.DeviceID)
		}
		return
	}
	cameraID, ok := s.cameraOfChannel(dc.DeviceID)
	if !ok {
		slog.Warn("gb28181-cascade: DeviceControl for unknown channel", "channel", dc.DeviceID)
		return
	}
	direction, speed, err := decodePTZCmd(dc.PTZCmd)
	if err != nil {
		slog.Warn("gb28181-cascade: unparseable PTZ command",
			"channel", dc.DeviceID, "ptz", dc.PTZCmd, "error", err)
		return
	}
	s.mu.Lock()
	fwd := s.ptzForward
	s.mu.Unlock()
	if fwd == nil {
		return
	}
	if err := fwd(cameraID, direction, speed); err != nil {
		slog.Warn("gb28181-cascade: PTZ forward failed",
			"channel", dc.DeviceID, "camera", cameraID, "direction", direction, "error", err)
	}
}

// decodePTZCmd decodes the hex-encoded 8-byte GB/T 28181 § A.4 PTZ command
// into a direction identifier and speed byte (byte3 = direction bits,
// byte4/5/6 = pan/tilt/zoom speeds — the largest non-zero wins).
func decodePTZCmd(hexCmd string) (string, byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(hexCmd))
	if err != nil {
		return "", 0, fmt.Errorf("bad hex: %w", err)
	}
	if len(raw) < 4 || raw[0] != 0xA5 {
		return "", 0, fmt.Errorf("not a PTZ command (len=%d first=0x%02x)", len(raw), firstByteOr(raw))
	}
	bits := raw[3]
	speed := byte(0)
	for _, b := range raw[4:min(7, len(raw))] {
		if b > speed {
			speed = b
		}
	}
	switch {
	case bits == 0x00:
		return "stop", 0, nil
	case bits == 0x10:
		return "zoom-in", speed, nil
	case bits == 0x20:
		return "zoom-out", speed, nil
	case bits&0x0F == 0:
		return "", 0, fmt.Errorf("direction bits 0x%02x carry no axis", bits)
	}
	var sb strings.Builder
	if bits&0x08 != 0 {
		sb.WriteString("up-")
	} else if bits&0x04 != 0 {
		sb.WriteString("down-")
	}
	if bits&0x02 != 0 {
		sb.WriteString("left")
	} else if bits&0x01 != 0 {
		sb.WriteString("right")
	}
	dir := strings.TrimSuffix(sb.String(), "-")
	if dir == "" {
		return "", 0, fmt.Errorf("direction bits 0x%02x carry no axis", bits)
	}
	return dir, speed, nil
}

func firstByteOr(b []byte) byte {
	if len(b) > 0 {
		return b[0]
	}
	return 0
}
