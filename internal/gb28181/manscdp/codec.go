package manscdp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"unicode/utf8"
)

// xmlHeader is the standard MANSCDP document declaration. GB/T 28181 § 9.4.1
// mandates encoding="GB2312"; our payloads are pure ASCII or UTF-8, both of
// which GB2312-aware devices accept.
var xmlHeader = []byte(`<?xml version="1.0" encoding="GB2312"?>` + "\n")

// Encode marshals v into a MANSCDP XML document with the standard declaration.
func Encode(v any) ([]byte, error) {
	body, err := xml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, xmlHeader...), body...), nil
}

// Decode parses a MANSCDP XML document and returns its CmdType plus the
// concrete decoded value (Catalog, Keepalive, DeviceInfo, DeviceStatus,
// RecordInfo, DeviceControl, or Alarm).
//
// The input is first parsed as UTF-8. If that fails — because the bytes are
// GBK/GB18030 encoded or the CmdType attribute cannot be determined — the
// input is converted via CharsetDecode and parsed again.
func Decode(data []byte) (CmdType, any, error) {
	// Fast path: bytes that are already valid UTF-8. Raw GBK/GB18030 bytes
	// must never reach xml.Unmarshal — it does not reliably reject invalid
	// UTF-8 inside character data.
	if utf8.Valid(data) {
		if ct, v, err := decodeOnce(data); err == nil {
			return ct, v, nil
		}
	}
	// Not valid UTF-8 (GBK/GB18030 from Chinese vendors) or the UTF-8
	// parse failed: convert the charset and re-parse.
	converted, cerr := CharsetDecode(data)
	if cerr != nil {
		return "", nil, cerr
	}
	return decodeOnce(converted)
}

// SSRC builds the GB/T 28181-2016 § 9.3.1.3 media stream SSRC: a leading
// digit (0 = live, 1 = playback) followed by the last 8 digits of the
// device ID. Shorter device IDs are used as-is.
func SSRC(playback bool, deviceID string) string {
	prefix := "0"
	if playback {
		prefix = "1"
	}
	if len(deviceID) > 8 {
		deviceID = deviceID[len(deviceID)-8:]
	}
	return prefix + deviceID
}

// decodeOnce parses data assuming UTF-8. It strips any XML declaration first
// (a declared non-UTF-8 charset would otherwise be rejected by encoding/xml),
// then routes on the CmdType attribute.
func decodeOnce(data []byte) (CmdType, any, error) {
	body := stripXMLDecl(data)
	var probe struct {
		CmdType CmdType `xml:"CmdType,attr"`
	}
	if err := xml.Unmarshal(body, &probe); err != nil {
		return "", nil, err
	}
	if probe.CmdType == "" {
		return "", nil, fmt.Errorf("manscdp: missing CmdType attribute")
	}
	switch probe.CmdType {
	case CmdCatalog:
		return unmarshalAs[Catalog](body, CmdCatalog)
	case CmdKeepalive:
		return unmarshalAs[Keepalive](body, CmdKeepalive)
	case CmdDeviceInfo:
		return unmarshalAs[DeviceInfo](body, CmdDeviceInfo)
	case CmdDeviceStatus:
		return unmarshalAs[DeviceStatus](body, CmdDeviceStatus)
	case CmdRecordInfo:
		return unmarshalAs[RecordInfo](body, CmdRecordInfo)
	case CmdDeviceControl:
		return unmarshalAs[DeviceControl](body, CmdDeviceControl)
	case CmdAlarm:
		return unmarshalAs[Alarm](body, CmdAlarm)
	default:
		return "", nil, fmt.Errorf("manscdp: unsupported CmdType %q", probe.CmdType)
	}
}

// unmarshalAs decodes body into a concrete T and pairs it with its CmdType.
func unmarshalAs[T any](body []byte, ct CmdType) (CmdType, any, error) {
	var v T
	if err := xml.Unmarshal(body, &v); err != nil {
		return "", nil, err
	}
	return ct, v, nil
}

// stripXMLDecl removes the <?xml ...?> prolog so encoding/xml does not reject
// documents whose declared charset differs from the (now converted) content.
func stripXMLDecl(data []byte) []byte {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("<?xml")) {
		return data
	}
	if end := bytes.Index(trimmed, []byte("?>")); end >= 0 {
		return trimmed[end+2:]
	}
	return data
}
