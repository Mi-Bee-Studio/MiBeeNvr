package sip

// QueryChannelRecords loopback test (#566): the platform's RecordInfo query
// to the device is correlated with the device's paged answer arriving as a
// separate SIP MESSAGE.

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"
)

var snRE = regexp.MustCompile(`<SN>(\d+)</SN>`)

func TestQueryChannelRecordsLoopback(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&platform.Device{ID: testDeviceID, NetAddr: client.conn.LocalAddr().String()})
	dev, ok := dm.Device(testDeviceID)
	require.True(t, ok)
	dev.Status.Store(platform.DeviceOnline)

	start := time.Now().Add(-2 * time.Hour)
	end := start.Add(time.Hour)

	type result struct {
		items []manscdp.RecordItem
		err   error
	}
	done := make(chan result, 1)
	go func() {
		items, err := srv.QueryChannelRecords(testDeviceID, fakeChannelID, start, end)
		done <- result{items, err}
	}()

	// Device side: ack the query and answer with one record.
	query := client.awaitRequest(sip.MESSAGE, 5*time.Second)
	client.respondRaw(query, 200, "OK", "", "")
	m := snRE.FindStringSubmatch(string(query.Body()))
	require.NotNil(t, m, "RecordInfo query must carry an SN: %q", query.Body())
	answer, err := manscdp.Encode(manscdp.RecordInfo{
		CmdType:  manscdp.CmdRecordInfo,
		SN:       mustAtoi(t, m[1]),
		DeviceID: fakeChannelID,
		SumNum:   1,
		RecordList: []manscdp.RecordItem{{
			DeviceID:  fakeChannelID,
			Name:      "Front Door",
			StartTime: start.Format("2006-01-02T15:04:05"),
			EndTime:   end.Format("2006-01-02T15:04:05"),
		}},
	})
	require.NoError(t, err)
	client.sendMessage(string(answer))

	select {
	case res := <-done:
		require.NoError(t, res.err)
		require.Len(t, res.items, 1)
		require.Equal(t, fakeChannelID, res.items[0].DeviceID)
	case <-time.After(5 * time.Second):
		t.Fatal("QueryChannelRecords did not return after the device answer")
	}

	// Stop the SIP server BEFORE the raw-UDP client socket closes (teardown
	// is LIFO: the client conn registered later would close first). With the
	// client already gone, the server's transaction layer can race a
	// transport error against transaction termination inside gosip — an
	// upstream closechan/chansend race that -race flags on slow CI runners.
	// Stopping here keeps server shutdown ahead of socket teardown.
	_ = srv.Stop()
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}
