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
// # GB/T 28181 version compatibility
//
// The platform (UAS) side targets GB/T 28181-2016 — the de-facto interop
// baseline virtually all deployed devices speak — and deliberately tolerates
// the practical differences of the other revisions:
//
//   - 2007: no digest auth (supported: leave gb28181.password empty and
//     REGISTERs are accepted unauthenticated); OPTIONS-based liveness
//     (answered 200 with an Allow set).
//   - 2011: same REGISTER/catalog/keepalive shape as 2016; devices that omit
//     the y= SSRC line in answers interoperate (the platform generates its
//     own 10-digit SSRC per 2016 Annex C.2.4 and does not require the echo).
//   - 2016: primary target — digest REGISTER auth, element-form MANSCDP
//     XML, GB2312/GBK/GB18030/UTF-8 bodies, 10-digit SSRC, catalog/keepalive,
//     UDP media with PS payloads.
//   - 2022: additive over 2016 (optional SDP f= line, subscription/notify
//     extensions, extra catalog fields). The platform omits f= (optional per
//     spec — devices tolerate its absence) and ignores unknown catalog
//     fields, so 2022 devices interoperate on the 2016 feature set. Control
//     additions are emitted too: FI lens instructions (§ A.3.3 iris/focus)
//     and auxiliary switches (§ A.3.7 wiper/light) ride the PTZCmd
//     transport, whose 8-byte layout is shared across revisions.
//
// Device-side tolerances accumulated from real-device interop (see
// Mi-Bee-Studio/mibee-eye-raspi issues #3-#6 for the firmware side):
//   - MANSCDP CmdType/SN accepted in BOTH element and attribute form
//   - video PES accepted with PES_header_data_length at byte 7 (legacy
//     firmware layout) OR byte 8 (ITU-T H.222.0), calibrated against the ES
//     start code; corrupt header lengths degrade to empty payloads, not panics
//   - RTP marker on the first packet of an AU (instead of the last) still
//     reassembles complete AUs; missing markers fall back to PS pack-header
//     boundaries via the forced-flush path
//   - PSM-less streams: codec resolved from parameter-set NALUs (slices
//     never decide — an H.264 slice 0x41 maps into the H.265 VPS slot under
//     the 6-bit shift)
//
// Tracking issue: #315
package gb28181

import _ "github.com/ghettovoice/gosip/sip"
