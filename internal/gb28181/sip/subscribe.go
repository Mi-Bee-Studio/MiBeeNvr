// GB/T 28181-2016 § 9.5 subscription engine: SUBSCRIBE Catalog/Alarm/
// MobilePosition with expiry refresh, SIP NOTIFY dispatch, and the alarm /
// mobile-position pipelines. Complements the periodic catalog poll
// (catalogLoop) with device-initiated pushes.

package sip

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// subscription refresh cadence: a subscription is renewed at 80% of its
// lifetime so a lost refresh still leaves a grace window before expiry.
const subscribeRefreshFraction = 80

// subCheckInterval is how often the refresh loop scans for due subscriptions.
const subCheckInterval = 30 * time.Second

// maxAlarmsPerDevice / maxPositionsPerDevice bound the in-memory rings
// exposed via the REST API (latest first).
const (
	maxAlarmsPerDevice    = 200
	maxPositionsPerDevice = 100
)

// gbSubscription tracks one active SUBSCRIBE (device + subject).
type gbSubscription struct {
	deviceID  string
	subject   manscdp.CmdType
	expires   time.Duration
	refreshAt time.Time
}

// subscriptionSubjects returns the configured subjects for a device.
func (s *Server) subscriptionSubjects() []manscdp.CmdType {
	var out []manscdp.CmdType
	if s.cfg.CatalogSubscriptionOn() {
		out = append(out, manscdp.CmdCatalog)
	}
	if s.cfg.AlarmSubscriptionOn() {
		out = append(out, manscdp.CmdAlarm)
	}
	if s.cfg.SubscribeMobilePosition {
		out = append(out, manscdp.CmdMobilePosition)
	}
	return out
}

// subscribeExpires resolves the configured subscription lifetime.
func (s *Server) subscribeExpires() time.Duration {
	if d, err := time.ParseDuration(s.cfg.SubscribeExpires); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

// subscribeDevice (re)subscribes every configured subject for a device.
// Called on REGISTER (and re-REGISTER) — the refresh loop keeps them alive.
func (s *Server) subscribeDevice(deviceID string) {
	for _, subject := range s.subscriptionSubjects() {
		if err := s.sendSubscribe(deviceID, subject); err != nil {
			slog.Debug("gb28181: subscribe failed", "device", deviceID, "subject", subject, "error", err)
		}
	}
}

// sendSubscribe sends one SIP SUBSCRIBE with the MANSCDP body and Expires
// header, and records the refresh deadline. Fire-and-forget: devices answer
// 200 (or 481 on expiry — the refresh loop re-subscribes either way).
func (s *Server) sendSubscribe(deviceID string, subject manscdp.CmdType) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}
	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()

	expires := s.subscribeExpires()
	body, err := manscdp.Encode(manscdp.Subscribe{
		CmdType:  subject,
		SN:       int(time.Now().UnixNano() % 100000),
		DeviceID: deviceID,
		Interval: 5, // MobilePosition report period (s); ignored otherwise
	})
	if err != nil {
		return err
	}

	serverHost := s.localIPFor(netAddr)
	eventHdr := &sip.GenericHeader{HeaderName: "Event", Contents: string(subject)}
	expHdr := sip.Expires(int(expires.Seconds()))
	if err := s.sendRequestTo(sip.SUBSCRIBE, deviceID, netAddr, serverHost,
		eventHdr, &expHdr, "Application/MANSCDP+xml", string(body)); err != nil {
		return err
	}

	s.subMu.Lock()
	s.subscriptions[deviceID+"|"+string(subject)] = &gbSubscription{
		deviceID:  deviceID,
		subject:   subject,
		expires:   expires,
		refreshAt: time.Now().Add(expires * subscribeRefreshFraction / 100),
	}
	s.subMu.Unlock()
	slog.Debug("gb28181: SUBSCRIBE sent", "device", deviceID, "subject", subject, "expires", expires)
	return nil
}

// subscribeLoop renews subscriptions before they expire until ctx cancels.
func (s *Server) subscribeLoop(ctx context.Context) {
	ticker := time.NewTicker(subCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			s.subMu.Lock()
			var due []*gbSubscription
			for _, sub := range s.subscriptions {
				if now.After(sub.refreshAt) {
					due = append(due, sub)
				}
			}
			s.subMu.Unlock()
			for _, sub := range due {
				dev, ok := s.deviceMgr.Device(sub.deviceID)
				if !ok || dev.Status.Load() != platform.DeviceOnline { // offline: re-REGISTER resubscribes
					continue
				}
				if err := s.sendSubscribe(sub.deviceID, sub.subject); err != nil {
					slog.Debug("gb28181: subscription refresh failed", "device", sub.deviceID, "subject", sub.subject, "error", err)
				}
			}
		}
	}
}

// unsubscribeDevice drops a device's subscriptions (unregister / teardown).
func (s *Server) unsubscribeDevice(deviceID string) {
	s.subMu.Lock()
	for key, sub := range s.subscriptions {
		if sub.deviceID == deviceID {
			delete(s.subscriptions, key)
		}
	}
	s.subMu.Unlock()
}

// handleNotify processes a SIP NOTIFY from a device: catalog-change, alarm,
// or mobile-position reports (GB/T 28181-2016 § 9.5).
func (s *Server) handleNotify(req sip.Request, tx sip.ServerTransaction) {
	body := req.Body()
	if body == "" {
		s.respond(req, tx, statusBadRequest, "Empty body", nil)
		return
	}
	ct, payload, err := manscdp.Decode([]byte(body))
	if err != nil {
		s.respond(req, tx, statusBadRequest, "Invalid MANSCDP body", nil)
		return
	}

	// Process BEFORE responding: a caller treating the 200 as "handled"
	// (tests asserting merged channels, alarm rings) must observe the
	// effects already applied.
	switch ct {
	case manscdp.CmdCatalog:
		p := payload.(manscdp.CatalogNotify)
		slog.Info("gb28181: catalog change notified", "device", p.DeviceID, "channels", len(p.Item))
		s.mergeCatalogChannels(p.DeviceID, p.Item)
	case manscdp.CmdAlarm:
		s.handleAlarm(payload.(manscdp.Alarm))
	case manscdp.CmdMobilePosition:
		s.handleMobilePosition(payload.(manscdp.MobilePosition))
	default:
		slog.Debug("gb28181: unhandled NOTIFY CmdType", "cmdtype", ct)
	}

	s.respond(req, tx, statusOK, "OK", nil)
}

// handleAlarm ingests one alarm notification: resolve the camera, publish to
// the event bus (SSE / notifications), and keep a ring for the REST API.
func (s *Server) handleAlarm(a manscdp.Alarm) {
	// The alarm body's DeviceID is the alarming channel (GB/T 28181-2016
	// § 9.5.2); group rings and camera lookup by the OWNING device.
	ownerID := s.deviceOfChannel(a.DeviceID)
	if ownerID == "" {
		ownerID = a.DeviceID
	}
	cameraID := ""
	if enrol := s.enroller(); enrol != nil {
		if id, ok := enrol.GB28181CameraIDByChannel(ownerID, a.DeviceID); ok {
			cameraID = id
		}
	}
	evt := event.GB28181AlarmEvent{
		CameraID:         cameraID,
		DeviceID:         a.DeviceID,
		ChannelID:        a.DeviceID,
		AlarmPriority:    a.AlarmPriority,
		AlarmMethod:      a.AlarmMethod,
		AlarmType:        a.AlarmType,
		AlarmTime:        a.AlarmTime,
		AlarmDescription: a.AlarmDescription,
		ReceivedAt:       time.Now(),
	}
	slog.Info("gb28181: alarm received", "device", a.DeviceID, "camera", cameraID,
		"priority", a.AlarmPriority, "method", a.AlarmMethod, "type", a.AlarmType)

	s.subMu.Lock()
	s.alarmRing[ownerID] = append([]event.GB28181AlarmEvent{evt}, s.alarmRing[ownerID]...)
	if len(s.alarmRing[ownerID]) > maxAlarmsPerDevice {
		s.alarmRing[ownerID] = s.alarmRing[ownerID][:maxAlarmsPerDevice]
	}
	s.subMu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(context.Background(), event.TopicGB28181Alarm, evt)
	}

	// Alarm-triggered streaming (#355): INVITE the alarming channel when it
	// is not already streaming (recording-owned sessions are left alone).
	s.alarmLinkage.Trigger(ownerID, a.DeviceID, s.cfg.AlarmLinkage)
}

// GB28181Alarms returns the device's most recent alarms (latest first).
func (s *Server) GB28181Alarms(deviceID string) []event.GB28181AlarmEvent {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	out := make([]event.GB28181AlarmEvent, len(s.alarmRing[deviceID]))
	copy(out, s.alarmRing[deviceID])
	return out
}

// handleMobilePosition stores one position report in the per-device ring.
func (s *Server) handleMobilePosition(p manscdp.MobilePosition) {
	pos := platform.GBPosition{
		DeviceID:  p.DeviceID,
		Time:      p.Time,
		Longitude: p.Longitude,
		Latitude:  p.Latitude,
		Speed:     p.Speed,
		Direction: p.Direction,
		Altitude:  p.Altitude,
		UpdatedAt: time.Now(),
	}
	s.subMu.Lock()
	s.posRing[p.DeviceID] = append([]platform.GBPosition{pos}, s.posRing[p.DeviceID]...)
	if len(s.posRing[p.DeviceID]) > maxPositionsPerDevice {
		s.posRing[p.DeviceID] = s.posRing[p.DeviceID][:maxPositionsPerDevice]
	}
	s.subMu.Unlock()
	slog.Debug("gb28181: mobile position received", "device", p.DeviceID,
		"lon", p.Longitude, "lat", p.Latitude, "speed", p.Speed)
}

// GB28181Positions returns the device's most recent positions (latest first).
func (s *Server) GB28181Positions(deviceID string) []platform.GBPosition {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	out := make([]platform.GBPosition, len(s.posRing[deviceID]))
	copy(out, s.posRing[deviceID])
	return out
}

// sendRequestTo assembles and sends a request to a device, mirroring
// SendMessage's addressing but with a configurable method and extra headers
// (SUBSCRIBE needs Event + Expires). contentType/body are set when non-empty.
func (s *Server) sendRequestTo(method sip.RequestMethod, deviceID, netAddr, serverHost string, eventHdr sip.Header, expiresHdr sip.Header, contentType, body string) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}

	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, err := strconv.Atoi(devPortStr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device port %q: %w", devPortStr, err)
	}
	portVal := sip.Port(devPort)

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerID}, FHost: serverHost},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: deviceID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: deviceID}, FHost: devHost, FPort: &portVal},
	}
	recipient := &sip.SipUri{FUser: sip.String{Str: deviceID}, FHost: devHost, FPort: &portVal}

	req, err := s.buildRequest(method, serverHost, from, to, recipient, "", contentType, body)
	if err != nil {
		return err
	}
	if eventHdr != nil {
		req.AppendHeader(eventHdr)
	}
	if expiresHdr != nil {
		req.AppendHeader(expiresHdr)
	}
	if _, err := srv.Request(req); err != nil {
		return fmt.Errorf("gb28181: send %s to %s: %w", method, deviceID, err)
	}
	return nil
}
