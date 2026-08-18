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
// GBK/GB18030 encoded or the CmdType element cannot be determined — the
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

// SSRC builds the GB/T 28181-2016 § 9.3.1.3 (Annex C.2.4) 10-digit decimal
// media stream SSRC: digit 1 = live(0)/playback(1), digits 2-6 = digits 4-8 of
// the 20-digit platform (server) ID, digits 7-10 = a per-stream sequence.
// serverID shorter than 8 digits is left-padded with zeros.
func SSRC(playback bool, serverID string, seq int) string {
	prefix := "0"
	if playback {
		prefix = "1"
	}
	return buildSSRC(prefix, serverID, seq)
}

// SSRCDownload builds the download-session SSRC variant (GB/T 28181-2022
// Annex C.2.4 extends the leading digit: 0=live, 1=playback, 2=download).
func SSRCDownload(serverID string, seq int) string {
	return buildSSRC("2", serverID, seq)
}

// buildSSRC assembles the 10-digit decimal SSRC from its parts.
func buildSSRC(prefix, serverID string, seq int) string {
	domain := "00000"
	if len(serverID) >= 8 {
		domain = serverID[3:8]
	} else if len(serverID) > 3 {
		domain = serverID[3:]
	}
	for len(domain) < 5 {
		domain = "0" + domain
	}
	return fmt.Sprintf("%s%s%04d", prefix, domain, seq%10000)
}

// decodeOnce parses data assuming UTF-8. It strips any XML declaration first
// (a declared non-UTF-8 charset would otherwise be rejected by encoding/xml),
// then routes on the CmdType child element. GB/T 28181-2016 § 9.3 encodes
// CmdType/SN as child elements — real devices (Hikvision, Dahua, Uniview)
// never send the attribute form.
//
// The root element (Response / Notify / Query / Control) disambiguates
// commands that share a CmdType: Catalog arrives as both a query Response and
// a subscription Notify, and TimeSync as a Query or a Response.
func decodeOnce(data []byte) (CmdType, any, error) {
	body := stripXMLDecl(data)
	var probe struct {
		XMLName     xml.Name
		CmdType     CmdType `xml:"CmdType"`
		CmdTypeAttr CmdType `xml:"CmdType,attr"`
	}
	if err := xml.Unmarshal(body, &probe); err != nil {
		return "", nil, err
	}
	if probe.CmdType == "" {
		probe.CmdType = probe.CmdTypeAttr
	}
	if probe.CmdType == "" {
		return "", nil, fmt.Errorf("manscdp: missing CmdType element")
	}
	switch {
	case probe.CmdType == CmdCatalog && probe.XMLName.Local == "Notify":
		return unmarshalAs[CatalogNotify](body, CmdCatalog)
	case probe.CmdType == CmdCatalog && probe.XMLName.Local == "Query":
		return unmarshalAs[CatalogQuery](body, CmdCatalog)
	case probe.CmdType == CmdTimeSync && probe.XMLName.Local == "Response":
		return unmarshalAs[TimeSyncResponse](body, CmdTimeSync)
	case probe.CmdType == CmdTimeSync:
		return unmarshalAs[TimeSyncQuery](body, CmdTimeSync)
	case probe.CmdType == CmdMobilePosition:
		return unmarshalAs[MobilePosition](body, CmdMobilePosition)
	case probe.CmdType == CmdRecordInfo && probe.XMLName.Local == "Query":
		// A platform's recording query carries CmdType RecordInfo under a
		// Query root; the Response-root form with the same CmdType is the
		// device's ANSWER. Without this arm the Query form fails to
		// unmarshal into RecordInfo (XMLName expects Response), so the
		// device side rejects the query with 400 and the upper platform
		// sees an empty recording list.
		return unmarshalAs[RecordInfoQuery](body, CmdRecordInfo)
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

// normalizer is implemented by every message type to coalesce attribute-form
// CmdType/SN aliases into the element fields (some minimal firmwares emit
// the attribute form).
type normalizer interface{ normalize() }

// unmarshalAs decodes body into a concrete T and pairs it with its CmdType.
func unmarshalAs[T any](body []byte, ct CmdType) (CmdType, any, error) {
	var v T
	if err := xml.Unmarshal(body, &v); err != nil {
		return "", nil, err
	}
	if n, ok := any(&v).(normalizer); ok {
		n.normalize()
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
