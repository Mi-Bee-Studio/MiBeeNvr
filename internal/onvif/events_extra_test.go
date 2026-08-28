// SPDX-License-Identifier: MIT
//
// EventSubscriber end-to-end against the mock SOAP server: create a PullPoint
// subscription, poll messages through the background goroutine, deliver them
// to the callback, renew before expiry, and unsubscribe.

package onvif

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const soapCreatePullPointSubscriptionResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tev:CreatePullPointSubscriptionResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl">
      <tev:SubscriptionReference><wsa:Address xmlns:wsa="http://www.w3.org/2005/08/addressing">SUBSCRIPTION_REF</wsa:Address></tev:SubscriptionReference>
      <wsnt:CurrentTime xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">CURRENT_TIME</wsnt:CurrentTime>
      <wsnt:TerminationTime xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">TERMINATION_TIME</wsnt:TerminationTime>
    </tev:CreatePullPointSubscriptionResponse>
  </s:Body>
</s:Envelope>`

const soapPullMessagesResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tev:PullMessagesResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl">
      <tev:NotificationMessage>
        <tev:Topic>tns1:VideoSource/MotionAlarm</tev:Topic>
        <tev:Message UtcTime="2026-01-02T03:04:05Z" PropertyOperation="Changed">
          <tev:Source><tev:SimpleItem Name="Source" Value="CAM"/></tev:Source>
          <tev:Data><tev:SimpleItem Name="State" Value="active"/></tev:Data>
        </tev:Message>
      </tev:NotificationMessage>
    </tev:PullMessagesResponse>
  </s:Body>
</s:Envelope>`

// startEventMockServer serves the full PullPoint lifecycle. The subscription
// reference points back at the same server so PullMessages/Renew/Unsubscribe
// all arrive on the one listener.
func startEventMockServer(t *testing.T, termination time.Time) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 8192)
		n, _ := r.Body.Read(body)
		b := string(body[:n])
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		switch {
		case strings.Contains(b, "GetCapabilities"):
			_, _ = w.Write([]byte(soapGetCapabilitiesResponse))
		case strings.Contains(b, "CreatePullPointSubscription"):
			resp := strings.ReplaceAll(soapCreatePullPointSubscriptionResponse,
				"SUBSCRIPTION_REF", srv.URL+"/sub-1")
			resp = strings.ReplaceAll(resp, "CURRENT_TIME", time.Now().UTC().Format(time.RFC3339))
			resp = strings.ReplaceAll(resp, "TERMINATION_TIME", termination.UTC().Format(time.RFC3339))
			_, _ = w.Write([]byte(resp))
		case strings.Contains(b, "PullMessages"):
			_, _ = w.Write([]byte(soapPullMessagesResponse))
		case strings.Contains(b, "Renew"):
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:RenewResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"><wsnt:TerminationTime>`)
			_, _ = w.Write([]byte(time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)))
			fmt.Fprint(w, `</wsnt:TerminationTime></wsnt:RenewResponse></s:Body></s:Envelope>`)
		case strings.Contains(b, "Unsubscribe"):
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:UnsubscribeResponse xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/></s:Body></s:Envelope>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(soapFaultResponse))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEventSubscriberLifecycle(t *testing.T) {
	// Termination must exceed SubscriptionRenewBefore (1h) or the first poll
	// tick renews instead of pulling.
	srv := startEventMockServer(t, time.Now().Add(2*time.Hour))

	client := NewClient(srv.URL, "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	eventsCh := make(chan ONVIFEvent, 4)
	// Use the concrete impl (the Client wrapper returns the narrow interface,
	// which lacks IsSubscribed) — same construction path the wrapper takes.
	sub := NewEventSubscriber(client.client,
		WithEventCallback(func(e ONVIFEvent) { eventsCh <- e }),
		withPollInterval(30*time.Millisecond),
		withPullTimeout(500*time.Millisecond),
	)
	require.NotNil(t, sub)
	require.False(t, sub.IsSubscribed("cam-1"))

	ctx := context.Background()
	require.NoError(t, sub.Subscribe(ctx, "cam-1"))
	require.True(t, sub.IsSubscribed("cam-1"))

	// Double subscribe is a no-op success.
	require.NoError(t, sub.Subscribe(ctx, "cam-1"))

	// The polling goroutine delivers the mock's motion event to the callback.
	require.Eventually(t, func() bool {
		select {
		case evt := <-eventsCh:
			require.Equal(t, "tns1:VideoSource/MotionAlarm", evt.Topic)
			require.Equal(t, "cam-1", evt.CameraID)
			require.Equal(t, "active", evt.Data["State"])
			require.Equal(t, "CAM", evt.Data["source.Source"])
			return true
		default:
			return false
		}
	}, 5*time.Second, 20*time.Millisecond, "polled event never reached callback")

	require.NoError(t, sub.Unsubscribe(ctx, "cam-1"))
	require.False(t, sub.IsSubscribed("cam-1"))
	// Unsubscribing again is a no-op.
	require.NoError(t, sub.Unsubscribe(ctx, "cam-1"))
}

func TestEventSubscriberSubscribeError(t *testing.T) {
	// Server faults every CreatePullPointSubscription → error surfaces.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 8192)
		n, _ := r.Body.Read(body)
		b := string(body[:n])
		if strings.Contains(b, "GetCapabilities") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapGetCapabilitiesResponse))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(soapFaultResponse))
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	sub := NewEventSubscriber(client.client, withPollInterval(10*time.Millisecond))
	err := sub.Subscribe(context.Background(), "cam-err")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PullPoint subscription")
	require.False(t, sub.IsSubscribed("cam-err"))
}

func TestEventSubscriberRenewalLoop(t *testing.T) {
	// Termination 50ms out and SubscriptionRenewBefore forces renewal before
	// the first pull; the Renew handler extends by an hour.
	srv := startEventMockServer(t, time.Now().Add(50*time.Millisecond))

	client := NewClient(srv.URL, "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))

	eventsCh := make(chan ONVIFEvent, 4)
	sub := NewEventSubscriber(client.client,
		WithEventCallback(func(e ONVIFEvent) { eventsCh <- e }),
		withPollInterval(20*time.Millisecond),
	)
	require.NoError(t, sub.Subscribe(context.Background(), "cam-renew"))

	// Renewal happens on the first poll tick; polling continues afterwards
	// (a failed renewal would stop the loop and deliver nothing).
	require.Eventually(t, func() bool {
		select {
		case <-eventsCh:
			return true
		default:
			return false
		}
	}, 5*time.Second, 20*time.Millisecond, "renewal-then-poll never delivered an event")
	require.True(t, sub.IsSubscribed("cam-renew"))

	sub.StopAll(context.Background())
	require.False(t, sub.IsSubscribed("cam-renew"))
}
