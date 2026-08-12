// Package gb28181 implements GB/T 28181 platform-role support for MiBee NVR.
//
// It provides SIP signaling (UAS), device/channel management, RTP media reception,
// MPEG-PS demuxing, and session orchestration so that GB28181-compliant IP cameras
// can register with the NVR and expose their channels as normal MiBee cameras.
//
// # SIP Dependency
//
// This package uses github.com/ghettovoice/gosip as the SIP stack. It was chosen
// because it is pure-Go (no CGO), supports UAS mode (answer-side), and is proven
// in production by Monibuca. The pin is recorded in go.mod.
//
// # Architecture
//
// The GB28181 implementation follows the existing ingest (push-in) pattern:
//   - SIP Server (pkg/app.Service "gb28181") owns the signaling lifecycle
//   - Device/Channel manager tracks registered devices and their channels
//   - SessionManager orchestrates INVITE/BYE media sessions (pull model)
//   - RTP Receiver + PS Demuxer extract NALUs from RTP/PS streams
//   - GB28181Recorder feeds NALUs to StreamHub (same as other recorders)
//
// Tracking issue: #315
package gb28181

import _ "github.com/ghettovoice/gosip/sip"
