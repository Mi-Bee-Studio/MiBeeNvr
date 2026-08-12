package manscdp

import (
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
		"<Response CmdType=\"Catalog\" SN=\"1\">" +
		"<DeviceID>34020000001310000001</DeviceID><SumNum>1</SumNum>" +
		"<DeviceList Num=\"1\"><Item><DeviceID>34020000001320000001</DeviceID>" +
		"<Name>通道一</Name><Status>ON</Status></Item></DeviceList></Response>")
	ct, v, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, CmdCatalog, ct)
	assert.Equal(t, "通道一", v.(Catalog).Item[0].Name)
}

func TestDecode_Malformed(t *testing.T) {
	_, _, err := Decode([]byte("<Response CmdType=\"Catalog\"><Unclosed>"))
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
	assert.Equal(t, "020000001", SSRC(false, "34020000001320000001"), "live: 0 + last 8 digits")
	assert.Equal(t, "120000001", SSRC(true, "34020000001320000001"), "playback: 1 + last 8 digits")
	assert.Equal(t, "012345678", SSRC(false, "12345678"), "8-digit ID used as-is")
	assert.Equal(t, "0123", SSRC(false, "123"), "short ID used as-is")
}
