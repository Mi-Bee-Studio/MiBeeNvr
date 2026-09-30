package onvif

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// WS-BaseNotification push subscriptions (#922): instead of the NVR polling
// PullMessages, the device POSTs wsnt:Notify messages to an HTTP consumer
// endpoint on the NVR (/api/onvif/notify/{cameraID}/{token}). Push is
// attempted first whenever the NVR has an externally reachable base URL
// configured (server.advertise_url); a device that answers the Subscribe
// probe with a SOAP fault keeps the Pull-Point path unchanged.

const (
	// DefaultPushDuration is the requested push subscription lifetime.
	DefaultPushDuration = time.Hour
	// pushProbeTimeout bounds the one-shot Subscribe probe — it runs inline
	// in the subscription reconcile, so it must not stall camera startup.
	pushProbeTimeout = 5 * time.Second
	// pushRenewFloor is the minimum delay before a half-TTL renew fires —
	// devices that grant very short terms must not turn the renew loop into
	// a busy cycle.
	pushRenewFloor = 2 * time.Second
	// nsEventsVer10 is the ver10 event service namespace used to resolve the
	// events XAddr from GetServices (same routing pattern as the media route).
	nsEventsVer10 = "http://www.onvif.org/ver10/events/wsdl"
)

// ErrPushNotSupported reports that the device answered the wsnt:Subscribe
// probe with a SOAP (Sender) fault or otherwise rejected push — the caller
// falls back to the Pull-Point subscription instead.
var ErrPushNotSupported = errors.New("onvif: device does not accept push event subscription")

// EventDeliverer is implemented by subscribers that receive events pushed
// from the device (the notify HTTP handler hands parsed events to the live
// subscriber so its counters/diagnostics stay meaningful).
type EventDeliverer interface {
	DeliverEvent(evt ONVIFEvent)
}

// pushSoapAPI posts a full SOAP envelope. An empty endpoint means "the
// device's events service" (resolved via GetServices); a non-empty endpoint
// is an explicit SubscriptionManager address (Renew/Unsubscribe live on the
// subscription's own manager endpoint, not the events service). The
// production implementation is (*Client).eventsSOAP; tests substitute an
// httptest-backed fake.
type pushSoapAPI func(ctx context.Context, endpoint, soapBody string) ([]byte, error)

// PushOption configures a PushSubscriber.
type PushOption func(*PushSubscriber)

// WithPushCallback sets the callback invoked for each event the device
// pushes. Required — a push subscription without a callback is useless.
func WithPushCallback(cb EventCallback) PushOption {
	return func(p *PushSubscriber) { p.callback = cb }
}

// WithPushNotifyURL sets the ConsumerReference address the device POSTs
// wsnt:Notify messages to (the NVR's notify endpoint including its token).
func WithPushNotifyURL(url string) PushOption {
	return func(p *PushSubscriber) { p.notifyURL = url }
}

// WithPushFallback sets the hook invoked when a live subscription degrades
// (renew failure) — the camera manager uses it to switch the camera back to
// the Pull-Point transport.
func WithPushFallback(f func(cameraID, reason string)) PushOption {
	return func(p *PushSubscriber) { p.onFallback = f }
}

// WithPushDuration overrides the requested subscription lifetime (tests).
func WithPushDuration(d time.Duration) PushOption {
	return func(p *PushSubscriber) { p.duration = d }
}

// PushSubscriber manages one camera's wsnt:Subscribe push subscription: it
// probes the device's event service with a raw Subscribe (ConsumerReference =
// the NVR notify endpoint), then keeps the subscription alive with half-TTL
// wsnt:Renew calls posted to the SubscriptionManager address. It satisfies
// EventSubscriber so the camera manager's subscriber registry, teardown and
// diagnostics work identically for both transports.
//
// Subscriptions are NOT persisted: after an NVR restart the old subscription
// is bounded by its TTL (and the device-side "3 failed deliveries" cleanup),
// and the new process simply subscribes again.
type PushSubscriber struct {
	soap       pushSoapAPI
	callback   EventCallback
	notifyURL  string
	onFallback func(cameraID, reason string)
	duration   time.Duration

	mu              sync.Mutex
	subscriptionRef string // SubscriptionReference address (diagnostics)
	managerAddr     string // SubscriptionManager address (Renew/Unsubscribe)
	terminationTime time.Time
	grantedDur      time.Duration
	state           string
	lastError       string
	eventCount      int64
	lastEventAt     time.Time
	lastEvent       string
	stopCh          chan struct{}
	unsubscribeOnce sync.Once
	fallbackOnce    sync.Once
}

var _ EventSubscriber = (*PushSubscriber)(nil)

// NewPushSubscriber builds a PushSubscriber over the given raw-SOAP transport.
func NewPushSubscriber(soap pushSoapAPI, opts ...PushOption) *PushSubscriber {
	p := &PushSubscriber{
		soap:     soap,
		duration: DefaultPushDuration,
		state:    StateResubscribing,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Subscribe probes the device with a raw wsnt:Subscribe. A SOAP fault maps to
// ErrPushNotSupported; on success the half-TTL renew loop starts.
func (p *PushSubscriber) Subscribe(ctx context.Context, cameraID string) error {
	if p.notifyURL == "" {
		return fmt.Errorf("push subscriber for camera %q: no notify URL configured", cameraID)
	}
	probeCtx, cancel := context.WithTimeout(ctx, pushProbeTimeout)
	defer cancel()

	resp, err := p.soap(probeCtx, "", buildSubscribeEnvelope(p.notifyURL, p.duration))
	if err != nil {
		return fmt.Errorf("push subscribe probe for camera %q: %w", cameraID, err)
	}
	subRef, mgrAddr, termination, ferr := parseSubscribeResponse(resp)
	if ferr != nil {
		if isSenderFault(ferr) {
			return fmt.Errorf("%w (camera %q): %w", ErrPushNotSupported, cameraID, ferr)
		}
		return fmt.Errorf("push subscribe for camera %q: %w", cameraID, ferr)
	}

	granted := time.Until(termination)
	if granted <= 0 {
		granted = p.duration
	}

	p.mu.Lock()
	p.subscriptionRef = subRef
	p.managerAddr = mgrAddr
	p.terminationTime = termination
	p.grantedDur = granted
	p.state = StateActive
	p.lastError = ""
	stopCh := make(chan struct{})
	p.stopCh = stopCh
	p.mu.Unlock()

	pushLogger.Info("created push subscription",
		"camera_id", cameraID,
		"subscription_ref", subRef,
		"granted", granted.String())

	go p.renewLoop(cameraID, stopCh)
	return nil
}

// renewLoop renews the subscription at half the granted TTL. Any renew
// failure trips the fallback hook once — the camera manager then degrades the
// camera to the Pull-Point transport.
func (p *PushSubscriber) renewLoop(cameraID string, stopCh chan struct{}) {
	for {
		p.mu.Lock()
		delay := p.grantedDur / 2
		p.mu.Unlock()
		if delay < pushRenewFloor {
			delay = pushRenewFloor
		}
		select {
		case <-stopCh:
			return
		case <-time.After(delay):
		}

		p.mu.Lock()
		addr := p.managerAddr
		termination := p.terminationTime
		p.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), pushProbeTimeout)
		resp, err := p.soap(ctx, addr, buildRenewEnvelope(p.duration))
		cancel()
		var newTerm time.Time
		if err == nil {
			newTerm, err = parseRenewResponse(resp)
		}
		if err != nil {
			p.mu.Lock()
			p.state = StateResubscribing
			p.lastError = err.Error()
			p.mu.Unlock()
			pushLogger.Warn("push subscription renew failed; degrading to pull-point",
				"camera_id", cameraID, "error", err)
			p.fallbackOnce.Do(func() {
				if p.onFallback != nil {
					p.onFallback(cameraID, err.Error())
				}
			})
			return
		}

		granted := time.Until(newTerm)
		if granted <= 0 {
			granted = time.Until(termination) / 2
		}
		p.mu.Lock()
		p.terminationTime = newTerm
		p.grantedDur = granted
		p.mu.Unlock()
	}
}

// DeliverEvent feeds one parsed Notify event into the callback and updates
// the diagnostics counters. Called by the notify HTTP handler.
func (p *PushSubscriber) DeliverEvent(evt ONVIFEvent) {
	p.mu.Lock()
	p.eventCount++
	p.lastEventAt = time.Now().UTC()
	p.lastEvent = formatRawEvent(evt)
	cb := p.callback
	p.mu.Unlock()
	if cb != nil {
		cb(evt)
	}
}

// GetEventMessages is part of the EventSubscriber interface. Push events
// arrive via the HTTP notify endpoint, not a client-side queue — always nil.
func (p *PushSubscriber) GetEventMessages(_ context.Context) ([]ONVIFEvent, error) {
	return nil, nil
}

// Status reports the push subscription diagnostics.
func (p *PushSubscriber) Status(string) EventSubscriptionStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return EventSubscriptionStatus{
		Subscribed:      p.state == StateActive,
		State:           p.state,
		Transport:       "push",
		SubscriptionRef: p.subscriptionRef,
		TerminationTime: p.terminationTime,
		EventCount:      p.eventCount,
		LastEventAt:     p.lastEventAt,
		LastEvent:       p.lastEvent,
		LastError:       p.lastError,
	}
}

// Unsubscribe stops the renew loop and fires a best-effort wsnt:Unsubscribe
// at the SubscriptionManager. Idempotent.
func (p *PushSubscriber) Unsubscribe(ctx context.Context, cameraID string) error {
	p.mu.Lock()
	stopCh := p.stopCh
	addr := p.managerAddr
	p.state = ""
	p.mu.Unlock()

	var err error
	p.unsubscribeOnce.Do(func() {
		if stopCh != nil {
			close(stopCh)
		}
		if addr != "" {
			unsubCtx, cancel := context.WithTimeout(ctx, pushProbeTimeout)
			_, serr := p.soap(unsubCtx, addr, buildUnsubscribeEnvelope())
			cancel()
			if serr != nil {
				pushLogger.Debug("push unsubscribe failed (TTL will reclaim it)",
					"camera_id", cameraID, "error", serr)
			}
			err = serr
		}
	})
	return err
}

// --- SOAP envelopes -------------------------------------------------------

func buildSubscribeEnvelope(consumerURL string, dur time.Duration) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">`)
	b.WriteString(`<s:Body>`)
	b.WriteString(`<wsnt:Subscribe xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">`)
	b.WriteString(`<wsnt:ConsumerReference><wsa:Address xmlns:wsa="http://www.w3.org/2005/08/addressing">`)
	xml.EscapeText(&b, []byte(consumerURL))
	b.WriteString(`</wsa:Address></wsnt:ConsumerReference>`)
	fmt.Fprintf(&b, `<wsnt:InitialTerminationTime>PT%.0fS</wsnt:InitialTerminationTime>`, dur.Seconds())
	b.WriteString(`</wsnt:Subscribe></s:Body></s:Envelope>`)
	return b.String()
}

// buildRenewEnvelope targets the SubscriptionManager address directly (the
// renew operation lives on the subscription's own manager endpoint, not the
// events service).
func buildRenewEnvelope(dur time.Duration) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">`)
	b.WriteString(`<s:Body><wsnt:Renew xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">`)
	fmt.Fprintf(&b, `<wsnt:TerminationTime>PT%.0fS</wsnt:TerminationTime>`, dur.Seconds())
	b.WriteString(`</wsnt:Renew></s:Body></s:Envelope>`)
	return b.String()
}

func buildUnsubscribeEnvelope() string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		`<s:Body><wsnt:Unsubscribe xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/>` +
		`</s:Body></s:Envelope>`
}

// --- response parsing -----------------------------------------------------

// soapFaultError distinguishes device-declined (Sender) faults from transport
// and other failures. isSenderFault reports the Sender subcode.
type soapFaultError struct {
	Text   string
	Sender bool
}

func (e *soapFaultError) Error() string { return e.Text }

func isSenderFault(err error) bool {
	var fe *soapFaultError
	return errors.As(err, &fe) && fe.Sender
}

// terminationLayouts covers the xsd:dateTime shapes devices actually emit.
var terminationLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999999",
}

func parseWSNTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range terminationLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseSubscribeResponse extracts the SubscriptionReference address, the
// SubscriptionManager address and the granted termination time.
func parseSubscribeResponse(body []byte) (subRef, mgrAddr string, termination time.Time, err error) {
	root, perr := parseXMLTree(body)
	if perr != nil {
		return "", "", time.Time{}, fmt.Errorf("parse SubscribeResponse: %w", perr)
	}
	if fault := findFault(root); fault != nil {
		return "", "", time.Time{}, fault
	}
	resp := root.findLocal("SubscribeResponse")
	if resp == nil {
		return "", "", time.Time{}, fmt.Errorf("parse SubscribeResponse: no SubscribeResponse element")
	}
	for _, ref := range append(resp.allLocal("SubscriptionReference"), resp.allLocal("SubscriptionManager")...) {
		if a := ref.firstLocal("Address"); a != nil {
			addr := strings.TrimSpace(a.text())
			if addr == "" {
				continue
			}
			if subRef == "" {
				subRef = addr
			} else if mgrAddr == "" && addr != subRef {
				mgrAddr = addr
			}
		}
	}
	if subRef == "" {
		return "", "", time.Time{}, fmt.Errorf("parse SubscribeResponse: no SubscriptionReference address")
	}
	if mgrAddr == "" {
		// Plenty of devices expose the same address for both roles.
		mgrAddr = subRef
	}
	if t := resp.firstLocal("TerminationTime"); t != nil {
		if parsed, ok := parseWSNTime(t.text()); ok {
			termination = parsed
		}
	}
	if termination.IsZero() {
		termination = time.Now().Add(DefaultPushDuration)
	}
	return subRef, mgrAddr, termination, nil
}

// parseRenewResponse extracts the granted termination time from a
// RenewResponse.
func parseRenewResponse(body []byte) (time.Time, error) {
	root, err := parseXMLTree(body)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse RenewResponse: %w", err)
	}
	if fault := findFault(root); fault != nil {
		return time.Time{}, fault
	}
	resp := root.findLocal("RenewResponse")
	if resp == nil {
		return time.Time{}, fmt.Errorf("parse RenewResponse: no RenewResponse element")
	}
	if t := resp.firstLocal("TerminationTime"); t != nil {
		if parsed, ok := parseWSNTime(t.text()); ok {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse RenewResponse: no TerminationTime")
}

// ParseNotifyEvents parses a wsnt:Notify SOAP body (what the device POSTs to
// the consumer endpoint) into events, mirroring the Pull-Point
// parseNotificationMessage semantics: Topic, Message/@UtcTime, Data items and
// source.* metadata. Tolerant of prefix dialects — element matching is by
// local name only (devices disagree on namespace prefixes).
func ParseNotifyEvents(body []byte, cameraID string) ([]ONVIFEvent, error) {
	root, err := parseXMLTree(body)
	if err != nil {
		return nil, fmt.Errorf("parse Notify: %w", err)
	}
	if len(root.children) == 0 {
		return nil, fmt.Errorf("parse Notify: body contains no XML elements")
	}
	if fault := findFault(root); fault != nil {
		return nil, fault
	}
	var msgs []*xmlNode
	root.collectLocal("NotificationMessage", &msgs)
	events := make([]ONVIFEvent, 0, len(msgs))
	for _, msg := range msgs {
		evt := ONVIFEvent{
			Data:     make(map[string]any),
			CameraID: cameraID,
		}
		if topic := msg.firstLocal("Topic"); topic != nil {
			evt.Topic = strings.TrimSpace(topic.text())
		}
		if m := msg.firstLocal("Message"); m != nil {
			if ts, ok := parseWSNTime(m.attr("UtcTime")); ok {
				evt.Timestamp = ts
			}
			if src := m.firstLocal("Source"); src != nil {
				for name, value := range parseItems(src) {
					evt.Data["source."+name] = value
				}
			}
			if data := m.firstLocal("Data"); data != nil {
				for name, value := range parseItems(data) {
					evt.Data[name] = value
				}
			}
		}
		if evt.Timestamp.IsZero() {
			evt.Timestamp = time.Now().UTC()
		}
		events = append(events, evt)
	}
	return events, nil
}

// parseItems reads SimpleItem children (Name/Value attributes — the ONVIF
// event payload form) plus literal Name/Value child pairs some devices emit.
func parseItems(parent *xmlNode) map[string]any {
	items := map[string]any{}
	for _, child := range parent.children {
		switch child.name {
		case "SimpleItem", "ElementItem":
			name := child.attr("Name")
			if name != "" {
				items[name] = child.attr("Value")
			}
		case "Name":
			// paired with a following Value sibling
			if child.next != nil && child.next.name == "Value" {
				items[strings.TrimSpace(child.text())] = child.next.text()
			}
		}
	}
	return items
}

// --- tolerant XML tree ----------------------------------------------------

// xmlNode is a local-name-keyed XML tree node: prefix dialects differ between
// devices, so all matching in this file is by local name.
type xmlNode struct {
	name     string
	attrs    []xml.Attr
	textBuf  strings.Builder
	children []*xmlNode
	next     *xmlNode // next sibling (Name/Value pair parsing)
	parent   *xmlNode
}

func localXMLName(full string) string {
	if idx := strings.Index(full, ":"); idx >= 0 {
		return full[idx+1:]
	}
	return full
}

// parseXMLTree builds a synthetic root whose children are the document's
// top-level elements.
func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	root := &xmlNode{name: "#document"}
	stack := []*xmlNode{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			node := &xmlNode{name: localXMLName(t.Name.Local), attrs: append([]xml.Attr(nil), t.Attr...), parent: top}
			if n := len(top.children); n > 0 {
				top.children[n-1].next = node
			}
			top.children = append(top.children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			top.textBuf.Write(t)
		}
	}
	return root, nil
}

func (n *xmlNode) text() string { return n.textBuf.String() }

// attr returns an attribute value by local name (namespace ignored).
func (n *xmlNode) attr(local string) string {
	for _, a := range n.attrs {
		if localXMLName(a.Name.Local) == local {
			return a.Value
		}
	}
	return ""
}

// firstLocal returns the first direct child with the given local name.
func (n *xmlNode) firstLocal(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// allLocal returns all direct children with the given local name.
func (n *xmlNode) allLocal(name string) []*xmlNode {
	var out []*xmlNode
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// findLocal returns the first node with the given local name anywhere in the
// subtree (document-level wrappers like Envelope/Body sit between the caller
// and the element it wants).
func (n *xmlNode) findLocal(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
		if found := c.findLocal(name); found != nil {
			return found
		}
	}
	return nil
}

// collectLocal gathers every node with the given local name in the subtree.
func (n *xmlNode) collectLocal(name string, out *[]*xmlNode) {
	for _, c := range n.children {
		if c.name == name {
			*out = append(*out, c)
		}
		c.collectLocal(name, out)
	}
}

// findFault searches the tree for a SOAP Fault and maps it to a
// soapFaultError (Sender faults flagged so Subscribe can classify them as
// ErrPushNotSupported).
func findFault(root *xmlNode) error {
	var walk func(n *xmlNode) error
	walk = func(n *xmlNode) error {
		if n.name == "Fault" {
			fe := &soapFaultError{Text: "SOAP fault"}
			// SOAP 1.2 layout: Fault/{Code/{Value,Subcode/{Value}},Reason/Text}.
			if code := n.firstLocal("Code"); code != nil {
				if v := code.firstLocal("Value"); v != nil {
					val := strings.TrimSpace(v.text())
					fe.Text = "SOAP fault: " + val
					if strings.Contains(val, "Sender") {
						fe.Sender = true
					}
				}
				if sub := code.firstLocal("Subcode"); sub != nil {
					if v := sub.firstLocal("Value"); v != nil {
						if subval := strings.TrimSpace(v.text()); subval != "" {
							fe.Text += ": " + subval
						}
					}
				}
			}
			if reason := n.firstLocal("Reason"); reason != nil {
				if txt := reason.firstLocal("Text"); txt != nil {
					if r := strings.TrimSpace(txt.text()); r != "" {
						fe.Text += ": " + r
					}
				}
			}
			return fe
		}
		for _, c := range n.children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

var pushLogger = eventLogger
