package manscdp

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

func TestDecode_Catalog(t *testing.T) {
	ct, v, err := Decode(readTestdata(t, "catalog_utf8.xml"))
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)

	cat, ok := v.(Catalog)
	require.True(t, ok)
	assert.Equal(t, 1, cat.SN)
	assert.Equal(t, "34020000001310000001", cat.DeviceID)
	assert.Equal(t, 2, cat.SumNum)
	require.Len(t, cat.Item, 2)
	assert.Equal(t, "34020000001320000001", cat.Item[0].DeviceID)
	assert.Equal(t, "通道一", cat.Item[0].Name)
	assert.Equal(t, "34020000001310000001", cat.Item[0].ParentID)
	assert.Equal(t, "ON", cat.Item[0].Status)
	assert.Equal(t, "Hikvision", cat.Item[0].Manufacturer)
	assert.Equal(t, 1, cat.Item[0].PTZType)
	assert.Equal(t, "通道二", cat.Item[1].Name)
	assert.Equal(t, "OFF", cat.Item[1].Status)
}

func TestDecode_CatalogGBK(t *testing.T) {
	// catalog_gbk.xml is the same Catalog with GBK-encoded channel names
	// (declared encoding="GB2312"), as emitted by Chinese camera vendors.
	data := readTestdata(t, "catalog_gbk.xml")
	ct, v, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)

	cat, ok := v.(Catalog)
	require.True(t, ok)
	require.Len(t, cat.Item, 2)
	assert.Equal(t, "通道一", cat.Item[0].Name)
	assert.Equal(t, "通道二", cat.Item[1].Name)
	assert.Equal(t, "ON", cat.Item[0].Status)
	assert.Equal(t, 1, cat.Item[0].PTZType)
}

func TestEncode_Catalog(t *testing.T) {
	in := Catalog{
		CmdType:  CmdCatalog,
		SN:       1,
		DeviceID: "34020000001310000001",
		SumNum:   1,
		Item: []Item{{
			DeviceID: "34020000001320000001",
			Name:     "通道一",
			ParentID: "34020000001310000001",
			Status:   "ON",
		}},
	}
	encoded, err := Encode(in)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "<?xml version=\"1.0\" encoding=\"GB2312\"?>")

	ct, v, err := Decode(encoded)
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)
	out := v.(Catalog)
	assert.Equal(t, in.CmdType, out.CmdType)
	assert.Equal(t, in.SN, out.SN)
	assert.Equal(t, in.DeviceID, out.DeviceID)
	assert.Equal(t, in.SumNum, out.SumNum)
	require.Len(t, out.Item, 1)
	assert.Equal(t, in.Item[0].DeviceID, out.Item[0].DeviceID)
	assert.Equal(t, in.Item[0].Name, out.Item[0].Name)
	assert.Equal(t, in.Item[0].ParentID, out.Item[0].ParentID)
	assert.Equal(t, in.Item[0].Status, out.Item[0].Status)
}

func TestDecode_RoutesAllCmdTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		ct   CmdType
	}{
		{
			name: "Catalog",
			in: Catalog{
				CmdType: CmdCatalog, SN: 1, DeviceID: "34020000001310000001", SumNum: 1,
				Item: []Item{{DeviceID: "34020000001320000001", Name: "通道一", Status: "ON"}},
			},
			ct: CmdCatalog,
		},
		{
			name: "Keepalive",
			in:   Keepalive{CmdType: CmdKeepalive, SN: 1, DeviceID: "34020000001310000001", Status: "OK"},
			ct:   CmdKeepalive,
		},
		{
			name: "DeviceInfo",
			in:   DeviceInfo{CmdType: CmdDeviceInfo, SN: 1, DeviceID: "34020000001310000001", Manufacturer: "Hikvision", Model: "DS-2CD", Firmware: "V4.0", Result: "OK"},
			ct:   CmdDeviceInfo,
		},
		{
			name: "DeviceStatus",
			in:   DeviceStatus{CmdType: CmdDeviceStatus, SN: 1, DeviceID: "34020000001310000001", Status: "ON", Time: "2026-08-12T10:00:00"},
			ct:   CmdDeviceStatus,
		},
		{
			name: "RecordInfo",
			in: RecordInfo{
				CmdType: CmdRecordInfo, SN: 1, DeviceID: "34020000001310000001", Name: "录像", SumNum: 1,
				RecordList: []RecordItem{{Name: "seg1", FilePath: "/rec/seg1.mp4", StartTime: "2026-08-12T10:00:00", EndTime: "2026-08-12T10:05:00", Type: "1"}},
			},
			ct: CmdRecordInfo,
		},
		{
			name: "DeviceControl",
			in:   DeviceControl{CmdType: CmdDeviceControl, SN: 1, DeviceID: "34020000001310000001", PTZCmd: "A50F010100000000"},
			ct:   CmdDeviceControl,
		},
		{
			name: "Alarm",
			in:   Alarm{CmdType: CmdAlarm, SN: 1, DeviceID: "34020000001310000001", AlarmPriority: "1", AlarmMethod: "2", AlarmTime: "2026-08-12T10:00:00", AlarmDescription: "motion"},
			ct:   CmdAlarm,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := Encode(tc.in)
			require.NoError(t, err)

			ct, v, err := Decode(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.ct, ct)
			assert.IsType(t, tc.in, v)

			// Re-encoding the decoded value must reproduce the document byte-for-byte.
			reencoded, err := Encode(v)
			require.NoError(t, err)
			assert.Equal(t, string(encoded), string(reencoded))
		})
	}
}

func TestDecode_CharsetDeclaredGB2312UTF8Content(t *testing.T) {
	// Real devices often declare encoding="GB2312" but actually send UTF-8.
	data := []byte("<?xml version=\"1.0\" encoding=\"GB2312\"?>\n" +
		"<Response>" +
		"<CmdType>Catalog</CmdType><SN>1</SN>" +
		"<DeviceID>34020000001310000001</DeviceID><SumNum>1</SumNum>" +
		"<DeviceList Num=\"1\"><Item><DeviceID>34020000001320000001</DeviceID>" +
		"<Name>通道一</Name><Status>ON</Status></Item></DeviceList></Response>")
	ct, v, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)
	assert.Equal(t, "通道一", v.(Catalog).Item[0].Name)
}

func TestDecode_Malformed(t *testing.T) {
	_, _, err := Decode([]byte("<Response><CmdType>Catalog</CmdType><Unclosed>"))
	require.Error(t, err)

	_, _, err = Decode([]byte("<Response><DeviceID>x</DeviceID></Response>"))
	require.Error(t, err, "missing CmdType must be an error, not a panic")
}

func TestCharsetDecode_UTF8(t *testing.T) {
	in := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Response>通道一</Response>")
	out, err := CharsetDecode(in)
	require.NoError(t, err)
	assert.Equal(t, in, out, "valid UTF-8 must pass through unchanged")
}

func TestCharsetDecode_GBK(t *testing.T) {
	// 通道一 in GBK bytes.
	gbk := []byte{0xCD, 0xA8, 0xB5, 0xC0, 0xD2, 0xBB}
	out, err := CharsetDecode(gbk)
	require.NoError(t, err)
	assert.Equal(t, "通道一", string(out))
}

func TestSSRC(t *testing.T) {
	// GB/T 28181-2016 Annex C.2.4: 10 digits = [0=live|1=playback] +
	// digits 4-8 of the platform ID + 4-digit sequence.
	assert.Equal(t, "0200000001", SSRC(false, "34020000002000000001", 1), "live SSRC from 20-digit platform ID")
	assert.Equal(t, "1200000042", SSRC(true, "34020000002000000001", 42), "playback SSRC")
	assert.Equal(t, "0200009999", SSRC(false, "34020000002000000001", 19999), "sequence wraps mod 10000")
	assert.Equal(t, "0000000007", SSRC(false, "", 7), "empty platform ID left-padded")
	assert.Len(t, SSRC(false, "34020000002000000001", 1), 10, "SSRC must be exactly 10 digits")
}

// TestDecode_CatalogAttributeForm is a compatibility test: some real devices
// (minimal firmwares) emit CmdType/SN as XML attributes instead of the
// spec's child elements — the decoder must accept both.
func TestDecode_CatalogAttributeForm(t *testing.T) {
	data := []byte(`<Response CmdType="Catalog" SN="7">` +
		`<DeviceID>34020000001310000001</DeviceID><SumNum>1</SumNum>` +
		`<DeviceList Num="1"><Item><DeviceID>34020000001320000001</DeviceID>` +
		`<Name>Front Gate</Name><Status>ON</Status></Item></DeviceList></Response>`)
	ct, v, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)
	cat := v.(Catalog)
	assert.Equal(t, 7, cat.SN)
	assert.Len(t, cat.Item, 1)
	assert.Equal(t, "Front Gate", cat.Item[0].Name)
}

// TestDecode_KeepaliveAttributeForm covers the attribute form for keepalives.
func TestDecode_KeepaliveAttributeForm(t *testing.T) {
	data := []byte(`<Notify CmdType="Keepalive" SN="3">` +
		`<DeviceID>34020000001310000001</DeviceID><Status>OK</Status></Notify>`)
	ct, v, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, CmdKeepalive, ct)
	ka := v.(Keepalive)
	assert.Equal(t, 3, ka.SN)
	assert.Equal(t, "OK", ka.Status)
}

func TestDecodeTimeSyncQuery(t *testing.T) {
	body := `<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>TimeSync</CmdType>
<SN>812</SN>
<DeviceID>34020000001310000003</DeviceID>
</Query>`
	ct, v, err := Decode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ct != CmdTimeSync {
		t.Fatalf("cmdtype = %q", ct)
	}
	q, ok := v.(TimeSyncQuery)
	if !ok {
		t.Fatalf("type %T", v)
	}
	if q.SN != 812 || q.DeviceID != "34020000001310000003" {
		t.Fatalf("fields: %+v", q)
	}
}

func TestEncodeDecodeTimeSyncResponse(t *testing.T) {
	raw, err := Encode(TimeSyncResponse{
		CmdType:  CmdTimeSync,
		SN:       812,
		DeviceID: "34020000001310000003",
		Time:     "2026-08-16T10:00:00",
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	ct, v, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ct != CmdTimeSync {
		t.Fatalf("cmdtype = %q", ct)
	}
	r, ok := v.(TimeSyncResponse)
	if !ok {
		t.Fatalf("type %T", v)
	}
	if r.Time != "2026-08-16T10:00:00" || r.SN != 812 {
		t.Fatalf("fields: %+v", r)
	}
	if !bytes.Contains(raw, []byte("<Response>")) {
		t.Fatalf("root not Response: %s", raw)
	}
}

func TestDecodeMobilePositionNotify(t *testing.T) {
	body := `<?xml version="1.0"?>
<Notify>
<CmdType>MobilePosition</CmdType>
<SN>1</SN>
<DeviceID>34020000001310000003</DeviceID>
<Time>2026-08-16T10:00:00</Time>
<Longitude>116.4074</Longitude>
<Latitude>39.9042</Latitude>
<Speed>12.5</Speed>
<Direction>45</Direction>
<Altitude>50.0</Altitude>
</Notify>`
	ct, v, err := Decode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ct != CmdMobilePosition {
		t.Fatalf("cmdtype = %q", ct)
	}
	p, ok := v.(MobilePosition)
	if !ok {
		t.Fatalf("type %T", v)
	}
	if p.Longitude != "116.4074" || p.Latitude != "39.9042" || p.Speed != "12.5" {
		t.Fatalf("fields: %+v", p)
	}
}

func TestDecodeCatalogNotifyVsResponse(t *testing.T) {
	notifyBody := `<?xml version="1.0"?>
<Notify>
<CmdType>Catalog</CmdType>
<SN>2</SN>
<DeviceID>34020000001310000003</DeviceID>
<SumNum>1</SumNum>
<DeviceList><Item><DeviceID>34020000001320000003</DeviceID><Name>ch1</Name><Status>ON</Status></Item></DeviceList>
</Notify>`
	ct, v, err := Decode([]byte(notifyBody))
	if err != nil {
		t.Fatalf("decode notify: %v", err)
	}
	if ct != CmdCatalog {
		t.Fatalf("cmdtype = %q", ct)
	}
	n, ok := v.(CatalogNotify)
	if !ok {
		t.Fatalf("notify decoded as %T", v)
	}
	if len(n.Item) != 1 || n.Item[0].DeviceID != "34020000001320000003" || n.Item[0].Status != "ON" {
		t.Fatalf("items: %+v", n.Item)
	}

	respBody := `<?xml version="1.0"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>3</SN>
<DeviceID>34020000001310000003</DeviceID>
<SumNum>1</SumNum>
<DeviceList><Item><DeviceID>34020000001320000003</DeviceID><Name>ch1</Name></Item></DeviceList>
</Response>`
	ct2, v2, err := Decode([]byte(respBody))
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ct2 != CmdCatalog {
		t.Fatalf("cmdtype = %q", ct2)
	}
	if _, ok := v2.(Catalog); !ok {
		t.Fatalf("response decoded as %T", v2)
	}
}

func TestEncodeSubscribe(t *testing.T) {
	raw, err := Encode(Subscribe{
		CmdType:  CmdMobilePosition,
		SN:       5,
		DeviceID: "34020000001310000003",
		Interval: 5,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{"<SUBSCRIBE>", "<CmdType>MobilePosition</CmdType>", "<Interval>5</Interval>"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}
