package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const soapNotifyStandard = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
      <wsnt:NotificationMessage>
        <wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">tns1:VideoSource/MotionAlarm</wsnt:Topic>
        <wsnt:Message>
          <tt:Source xmlns:tt="http://www.onvif.org/ver10/schema"><tt:SimpleItem Name="Source" Value="CSI"/></tt:Source>
          <tt:Data xmlns:tt="http://www.onvif.org/ver10/schema">
            <tt:SimpleItem Name="State" Value="true"/>
            <tt:SimpleItem Name="Score" Value="73"/>
          </tt:Data>
        </wsnt:Message>
      </wsnt:NotificationMessage>
      <wsnt:NotificationMessage>
        <wsnt:Topic>tns1:Device/Tamper</wsnt:Topic>
        <wsnt:Message UtcTime="2026-09-30T08:00:00Z">
          <tt:Data xmlns:tt="http://www.onvif.org/ver10/schema"><tt:SimpleItem Name="State" Value="false"/></tt:Data>
        </wsnt:Message>
      </wsnt:NotificationMessage>
    </wsnt:Notify>
  </s:Body>
</s:Envelope>`

func TestParseNotifyEventsStandardDialect(t *testing.T) {
	t.Parallel()
	events, err := ParseNotifyEvents([]byte(soapNotifyStandard), "cam-x")
	require.NoError(t, err)
	require.Len(t, events, 2)

	first := events[0]
	require.Equal(t, "tns1:VideoSource/MotionAlarm", first.Topic)
	require.Equal(t, "cam-x", first.CameraID)
	require.Equal(t, "true", first.Data["State"])
	require.Equal(t, "73", first.Data["Score"])
	require.Equal(t, "CSI", first.Data["source.Source"])
	require.False(t, first.Timestamp.IsZero(), "missing UtcTime falls back to now")

	second := events[1]
	require.Equal(t, "tns1:Device/Tamper", second.Topic)
	require.Equal(t, "false", second.Data["State"])
	want := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	require.True(t, second.Timestamp.Equal(want), "UtcTime attribute must be parsed, got %v", second.Timestamp)
}

func TestParseNotifyEventsPrefixDialects(t *testing.T) {
	t.Parallel()
	// rs-style dialect: different prefixes, literal Name/Value pairs instead
	// of SimpleItem attributes.
	body := `<?xml version="1.0"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope"><env:Body>
<nt:Notify xmlns:nt="http://docs.oasis-open.org/wsn/b-2">
 <nt:NotificationMessage>
  <nt:Topic>tns1:VideoSource/MotionAlarm</nt:Topic>
  <nt:Message UtcTime="2026-09-30T08:00:00.5Z">
   <onvif:Source xmlns:onvif="http://www.onvif.org/ver10/schema"><onvif:Name>Source</onvif:Name><onvif:Value>CSI</onvif:Value></onvif:Source>
   <onvif:Data xmlns:onvif="http://www.onvif.org/ver10/schema"><onvif:Name>State</onvif:Name><onvif:Value>true</onvif:Value></onvif:Data>
  </nt:Message>
 </nt:NotificationMessage>
</nt:Notify>
</env:Body></env:Envelope>`
	events, err := ParseNotifyEvents([]byte(body), "cam-x")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "true", events[0].Data["State"])
	require.Equal(t, "CSI", events[0].Data["source.Source"])
}

func TestParseNotifyEventsCanonicalDoubleLayer(t *testing.T) {
	t.Parallel()
	// Canonical ONVIF double-layer payload: wsnt:Message wrapping an inner
	// payload element that carries @UtcTime and the Source/Key/Data groups.
	// Byte shape mirrors the rs device wire format (onvif-device-rs writer):
	// SubscriptionReference sibling present, PropertyOperation stamped, Key
	// group empty-inline — and the device's habit of wrapping the whole
	// response envelope inside another envelope rides along harmlessly.
	body := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body><?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body>
<wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:wsa="http://www.w3.org/2005/08/addressing" xmlns:tt="http://www.onvif.org/ver10/schema">
 <wsnt:NotificationMessage>
  <wsnt:SubscriptionReference><wsa:Address>http://192.0.2.10:8080/onvif/events_service/sub/abc</wsa:Address></wsnt:SubscriptionReference>
  <wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/Concrete">tns1:VideoSource/MotionAlarm</wsnt:Topic>
  <wsnt:Message>
   <tt:Message PropertyOperation="Changed" UtcTime="2026-10-01T07:46:12.123Z">
    <tt:Source><tt:SimpleItem Name="Source" Value="0"/></tt:Source>
    <tt:Key></tt:Key>
    <tt:Data>
     <tt:SimpleItem Name="State" Value="true"/>
     <tt:SimpleItem Name="Targets" Value="person"/>
    </tt:Data>
   </tt:Message>
  </wsnt:Message>
 </wsnt:NotificationMessage>
</wsnt:Notify>
</soap:Body></soap:Envelope>
</soap:Body></soap:Envelope>`
	events, err := ParseNotifyEvents([]byte(body), "cam-x")
	require.NoError(t, err)
	require.Len(t, events, 1)

	evt := events[0]
	require.Equal(t, "tns1:VideoSource/MotionAlarm", evt.Topic)
	require.Equal(t, "true", evt.Data["State"])
	require.Equal(t, "person", evt.Data["Targets"])
	require.Equal(t, "0", evt.Data["source.Source"])
	want := time.Date(2026, 10, 1, 7, 46, 12, 123000000, time.UTC)
	require.True(t, evt.Timestamp.Equal(want), "inner @UtcTime must be parsed, got %v", evt.Timestamp)
}

func TestParseNotifyEventsEmptyAndFault(t *testing.T) {
	t.Parallel()
	events, err := ParseNotifyEvents([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/></s:Body></s:Envelope>`), "cam-x")
	require.NoError(t, err)
	require.Empty(t, events, "empty Notify (heartbeat) is valid")

	_, err = ParseNotifyEvents([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:Sender</s:Value></s:Code></s:Fault></s:Body></s:Envelope>`), "cam-x")
	require.Error(t, err)
	require.True(t, isSenderFault(err), "faults inside Notify must surface as soap faults, got %v", err)
}

func TestParseSubscribeResponse(t *testing.T) {
	t.Parallel()
	body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<wsnt:SubscribeResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
 <wsnt:SubscriptionReference><wsa:Address xmlns:wsa="http://www.w3.org/2005/08/addressing">http://camera/sub-1</wsa:Address></wsnt:SubscriptionReference>
 <wsnt:CurrentTime>%s</wsnt:CurrentTime>
 <wsnt:TerminationTime>%s</wsnt:TerminationTime>
</wsnt:SubscribeResponse></s:Body></s:Envelope>`,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339))

	subRef, mgr, term, err := parseSubscribeResponse([]byte(body))
	require.NoError(t, err)
	require.Equal(t, "http://camera/sub-1", subRef)
	require.Equal(t, "http://camera/sub-1", mgr, "single address serves both roles")
	require.True(t, time.Until(term) > 50*time.Minute)
}

func TestParseSubscribeResponseSenderFault(t *testing.T) {
	t.Parallel()
	body := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:ActionNotSupported</s:Value></s:Subcode></s:Code>
<s:Reason><s:Text>Subscribe not supported</s:Text></s:Reason></s:Fault>
</s:Body></s:Envelope>`
	_, _, _, err := parseSubscribeResponse([]byte(body))
	require.Error(t, err)
	require.True(t, isSenderFault(err))
}

// pushTestDevice is a minimal events-service stand-in: it accepts
// wsnt:Subscribe/Renew/Unsubscribe and records what the subscriber sent.
type pushTestDevice struct {
	mu           sync.Mutex
	subscribeBs  []string
	renews       int
	renewFail    bool
	unsubscribes int
	granted      time.Duration
	srv          *httptest.Server
}

func newPushTestDevice(t *testing.T, granted time.Duration) *pushTestDevice {
	t.Helper()
	d := &pushTestDevice{granted: granted}
	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/events", func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		d.mu.Lock()
		d.subscribeBs = append(d.subscribeBs, body)
		d.mu.Unlock()
		fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<wsnt:SubscribeResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<wsnt:SubscriptionReference><wsa:Address>%s/onvif/sub-1</wsa:Address></wsnt:SubscriptionReference>
<wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime>
</wsnt:SubscribeResponse></s:Body></s:Envelope>`,
			d.srvURL(), time.Now().UTC().Format(time.RFC3339),
			time.Now().Add(d.granted).UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("/onvif/sub-1", func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case strings.Contains(body, "Renew"):
			d.renews++
			if d.renewFail {
				fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:Sender</s:Value></s:Code></s:Fault></s:Body></s:Envelope>`)
				return
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:RenewResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"><wsnt:TerminationTime>%s</wsnt:TerminationTime></wsnt:RenewResponse></s:Body></s:Envelope>`,
				time.Now().Add(d.granted).UTC().Format(time.RFC3339))
		case strings.Contains(body, "Unsubscribe"):
			d.unsubscribes++
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:UnsubscribeResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/></s:Body></s:Envelope>`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	})
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	return d
}

func (d *pushTestDevice) srvURL() string { return d.srv.URL }

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	buf, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	return string(buf)
}

// soap posts to the events endpoint unless an explicit endpoint (the
// SubscriptionManager address) is given — mirrors (*Client).eventsSOAP.
func (d *pushTestDevice) soap(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	target := d.srvURL() + "/onvif/events"
	if endpoint != "" {
		target = endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(soapBody))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(buf))
	}
	return buf, nil
}

func TestPushSubscriberLifecycle(t *testing.T) {
	t.Parallel()
	dev := newPushTestDevice(t, 4*time.Second)

	var mu sync.Mutex
	var got []ONVIFEvent
	sub := NewPushSubscriber(dev.soap,
		WithPushCallback(func(evt ONVIFEvent) { mu.Lock(); got = append(got, evt); mu.Unlock() }),
		WithPushNotifyURL("http://nvr.test:9090/api/onvif/notify/cam-x/tok123"),
		WithPushDuration(time.Hour),
	)
	require.NoError(t, sub.Subscribe(context.Background(), "cam-x"))

	// The probe must carry the consumer URL and a termination request.
	dev.mu.Lock()
	require.NotEmpty(t, dev.subscribeBs)
	probe := dev.subscribeBs[0]
	dev.mu.Unlock()
	require.Contains(t, probe, "http://nvr.test:9090/api/onvif/notify/cam-x/tok123")
	require.Contains(t, probe, "wsnt:Subscribe")
	require.Contains(t, probe, "PT3600S")

	st := sub.Status("cam-x")
	require.True(t, st.Subscribed)
	require.Equal(t, "push", st.Transport)

	// Delivered notify events reach the callback and the counters.
	events, err := ParseNotifyEvents([]byte(soapNotifyStandard), "cam-x")
	require.NoError(t, err)
	for _, evt := range events {
		sub.DeliverEvent(evt)
	}
	mu.Lock()
	require.Len(t, got, 2)
	mu.Unlock()
	require.Equal(t, int64(2), sub.Status("cam-x").EventCount)

	// Renew fires within the granted/2 window (floor 2s).
	require.Eventually(t, func() bool {
		dev.mu.Lock()
		defer dev.mu.Unlock()
		return dev.renews >= 1
	}, 10*time.Second, 200*time.Millisecond, "half-TTL renew must fire")

	require.NoError(t, sub.Unsubscribe(context.Background(), "cam-x"))
	dev.mu.Lock()
	defer dev.mu.Unlock()
	require.Equal(t, 1, dev.unsubscribes, "teardown must send wsnt:Unsubscribe to the manager address")
	require.False(t, sub.Status("cam-x").Subscribed)
}

func TestPushSubscriberRenewFailureDegrades(t *testing.T) {
	t.Parallel()
	dev := newPushTestDevice(t, 2*time.Second)

	fallbackCh := make(chan string, 1)
	sub := NewPushSubscriber(dev.soap,
		WithPushCallback(func(ONVIFEvent) {}),
		WithPushNotifyURL("http://nvr.test:9090/api/onvif/notify/cam-x/tok"),
		WithPushFallback(func(cameraID, reason string) { fallbackCh <- reason }),
	)
	require.NoError(t, sub.Subscribe(context.Background(), "cam-x"))

	dev.mu.Lock()
	dev.renewFail = true
	dev.mu.Unlock()

	select {
	case reason := <-fallbackCh:
		require.NotEmpty(t, reason)
	case <-time.After(10 * time.Second):
		t.Fatal("renew failure must trip the fallback hook")
	}
	require.Equal(t, StateResubscribing, sub.Status("cam-x").State)
}

func TestPushSubscriberProbeSenderFaultMapsToNotSupported(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:ActionNotSupported</s:Value></s:Subcode></s:Code></s:Fault>
</s:Body></s:Envelope>`)
	}))
	t.Cleanup(srv.Close)

	soap := func(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(soapBody))
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		buf, _ := io.ReadAll(resp.Body)
		return buf, nil
	}
	sub := NewPushSubscriber(soap, WithPushCallback(func(ONVIFEvent) {}), WithPushNotifyURL("http://nvr/x"))
	err := sub.Subscribe(context.Background(), "cam-x")
	require.ErrorIs(t, err, ErrPushNotSupported)
}
