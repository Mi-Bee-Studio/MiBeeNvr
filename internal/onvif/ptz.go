package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"sync"

	onvifgo "github.com/mickeyzzc/onvif-go/v2"
)

var ptzLogger = slog.Default().With("component", "onvif-ptz")

// PTZControllerImpl implements PTZController by delegating to onvif-go's PTZ service.
// It wraps an onvif-go Client and stores the profile token internally.
type PTZControllerImpl struct {
	client       *onvifgo.Client
	profileToken string
	endpoint     string
	username     string
	password     string
	rawSOAP      func(ctx context.Context, endpoint, soapBody string) ([]byte, error)
	mu           sync.Mutex
}

// NewPTZController creates a PTZController backed by an onvif-go client
// with raw SOAP fallback support for cameras that reject WS-Security.
func NewPTZController(client *onvifgo.Client, profileToken, endpoint, username, password string, rawSOAPFn func(context.Context, string, string) ([]byte, error)) *PTZControllerImpl {
	return &PTZControllerImpl{
		client:       client,
		profileToken: profileToken,
		endpoint:     endpoint,
		username:     username,
		password:     password,
		rawSOAP:      rawSOAPFn,
	}
}

// SetProfileToken updates the ONVIF media profile token used for PTZ commands.
func (p *PTZControllerImpl) SetProfileToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.profileToken = token
}

// ContinuousMove starts continuous PTZ movement at the given velocity.
func (p *PTZControllerImpl) ContinuousMove(ctx context.Context, velocity PTZVector) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Provide default timeout — some cameras reject ContinuousMove without it
	timeout := "PT10S"
	return p.client.PTZ().ContinuousMove(ctx, p.profileToken, toOnvifPTZSpeed(velocity), &timeout)
}

// AbsoluteMove moves PTZ to an absolute position.
// Falls back to raw SOAP without auth if onvif-go gets an auth error.
func (p *PTZControllerImpl) AbsoluteMove(ctx context.Context, position PTZVector) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.client.PTZ().AbsoluteMove(ctx, p.profileToken, toOnvifPTZVector(position), nil)
	if err == nil || !isAuthError(err) {
		return err
	}
	ptzLogger.Warn("PTZ auth rejected, retrying with PasswordText WS-Security", "operation", "AbsoluteMove")
	return p.rawAbsoluteMove(ctx, position)
}

// RelativeMove moves PTZ relative to the current position.
// Falls back to raw SOAP without auth if onvif-go gets an auth error.
func (p *PTZControllerImpl) RelativeMove(ctx context.Context, displacement PTZVector) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.client.PTZ().RelativeMove(ctx, p.profileToken, toOnvifPTZVector(displacement), nil)
	if err == nil || !isAuthError(err) {
		return err
	}
	ptzLogger.Warn("PTZ auth rejected, retrying with PasswordText WS-Security", "operation", "RelativeMove")
	return p.rawRelativeMove(ctx, displacement)
}

// Stop stops PTZ movement. stopPanTilt and stopZoom control which axes to stop.
func (p *PTZControllerImpl) Stop(ctx context.Context, stopPanTilt, stopZoom bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.PTZ().Stop(ctx, p.profileToken, stopPanTilt, stopZoom)
}

// GetStatus returns the current PTZ position and whether the camera is moving.
// Falls back to raw SOAP without auth if onvif-go gets an auth error.
func (p *PTZControllerImpl) GetStatus(ctx context.Context) (position PTZVector, moving bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status, err := p.client.PTZ().GetStatus(ctx, p.profileToken)
	if err == nil || !isAuthError(err) {
		return fromOnvifPTZStatus(status)
	}
	ptzLogger.Warn("PTZ auth rejected, retrying with PasswordText WS-Security", "operation", "GetStatus")
	return p.rawGetStatus(ctx)
}

// GetPresets returns all PTZ presets on the camera.
func (p *PTZControllerImpl) GetPresets(ctx context.Context) ([]PTZPreset, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	presets, err := p.client.PTZ().GetPresets(ctx, p.profileToken)
	if err != nil {
		return nil, fmt.Errorf("get PTZ presets failed: %w", err)
	}
	result := make([]PTZPreset, len(presets))
	for i, preset := range presets {
		result[i] = PTZPreset{
			Token: preset.Token,
			Name:  preset.Name,
		}
		if preset.PTZPosition != nil {
			result[i].Position = fromOnvifPTZVector(preset.PTZPosition)
		}
	}
	return result, nil
}

// SetPreset creates a new PTZ preset at the current position.
// Falls back to raw SOAP without auth if onvif-go gets an auth error.
func (p *PTZControllerImpl) SetPreset(ctx context.Context, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	token, err := p.client.PTZ().SetPreset(ctx, p.profileToken, name, "")
	if err == nil || !isAuthError(err) {
		return token, err
	}
	ptzLogger.Warn("PTZ auth rejected, retrying with PasswordText WS-Security", "operation", "SetPreset")
	return p.rawSetPreset(ctx, name)
}

// GoToPreset moves the camera to a saved preset position.
func (p *PTZControllerImpl) GoToPreset(ctx context.Context, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.PTZ().GotoPreset(ctx, p.profileToken, token, nil)
}

// RemovePreset deletes a PTZ preset.
func (p *PTZControllerImpl) RemovePreset(ctx context.Context, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.PTZ().RemovePreset(ctx, p.profileToken, token)
}

// --- Type conversion helpers ---

func toOnvifPTZVector(v PTZVector) *onvifgo.PTZVector {
	return &onvifgo.PTZVector{
		PanTilt: &onvifgo.Vector2D{X: v.Pan, Y: v.Tilt},
		Zoom:    &onvifgo.Vector1D{X: v.Zoom},
	}
}

func toOnvifPTZSpeed(v PTZVector) *onvifgo.PTZSpeed {
	return &onvifgo.PTZSpeed{
		PanTilt: &onvifgo.Vector2D{X: v.Pan, Y: v.Tilt},
		Zoom:    &onvifgo.Vector1D{X: v.Zoom},
	}
}

func fromOnvifPTZVector(v *onvifgo.PTZVector) PTZVector {
	result := PTZVector{}
	if v != nil {
		if v.PanTilt != nil {
			result.Pan = v.PanTilt.X
			result.Tilt = v.PanTilt.Y
		}
		if v.Zoom != nil {
			result.Zoom = v.Zoom.X
		}
	}
	return result
}

func fromOnvifPTZStatus(s *onvifgo.PTZStatus) (PTZVector, bool, error) {
	var pos PTZVector
	var moving bool
	if s != nil {
		pos = fromOnvifPTZVector(s.Position)
		if s.MoveStatus != nil {
			moving = s.MoveStatus.PanTilt == "MOVING" || s.MoveStatus.Zoom == "MOVING"
		}
	}
	return pos, moving, nil
}

// --- Raw SOAP fallback helpers (no-auth, for camera firmware that rejects WS-Security) ---

func (p *PTZControllerImpl) rawAbsoluteMove(ctx context.Context, position PTZVector) error {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tptz:AbsoluteMove>
      <tptz:ProfileToken>%s</tptz:ProfileToken>
      <tptz:Position>
        <tt:PanTilt x="%f" y="%f"/>
        <tt:Zoom x="%f"/>
      </tptz:Position>
    </tptz:AbsoluteMove>
  </s:Body>
</s:Envelope>`, p.profileToken, position.Pan, position.Tilt, position.Zoom)

	if p.rawSOAP == nil {
		return fmt.Errorf("raw SOAP fallback not available")
	}
	_, err := p.rawSOAP(ctx, p.endpoint, soapBody)
	if err != nil {
		return fmt.Errorf("raw AbsoluteMove failed: %w", err)
	}
	return nil
}

func (p *PTZControllerImpl) rawRelativeMove(ctx context.Context, displacement PTZVector) error {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tptz:RelativeMove>
      <tptz:ProfileToken>%s</tptz:ProfileToken>
      <tptz:Translation>
        <tt:PanTilt x="%f" y="%f"/>
        <tt:Zoom x="%f"/>
      </tptz:Translation>
    </tptz:RelativeMove>
  </s:Body>
</s:Envelope>`, p.profileToken, displacement.Pan, displacement.Tilt, displacement.Zoom)

	if p.rawSOAP == nil {
		return fmt.Errorf("raw SOAP fallback not available")
	}
	_, err := p.rawSOAP(ctx, p.endpoint, soapBody)
	if err != nil {
		return fmt.Errorf("raw RelativeMove failed: %w", err)
	}
	return nil
}

func (p *PTZControllerImpl) rawGetStatus(ctx context.Context) (PTZVector, bool, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
  <s:Body>
    <tptz:GetStatus>
      <tptz:ProfileToken>%s</tptz:ProfileToken>
    </tptz:GetStatus>
  </s:Body>
</s:Envelope>`, p.profileToken)

	if p.rawSOAP == nil {
		return PTZVector{}, false, fmt.Errorf("raw SOAP fallback not available")
	}
	body, err := p.rawSOAP(ctx, p.endpoint, soapBody)
	if err != nil {
		return PTZVector{}, false, fmt.Errorf("raw GetStatus failed: %w", err)
	}
	return parseRawGetStatusResponse(body)
}

func (p *PTZControllerImpl) rawSetPreset(ctx context.Context, name string) (string, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl">
  <s:Body>
    <tptz:SetPreset>
      <tptz:ProfileToken>%s</tptz:ProfileToken>
      <tptz:PresetName>%s</tptz:PresetName>
    </tptz:SetPreset>
  </s:Body>
</s:Envelope>`, p.profileToken, name)

	if p.rawSOAP == nil {
		return "", fmt.Errorf("raw SOAP fallback not available")
	}
	body, err := p.rawSOAP(ctx, p.endpoint, soapBody)
	if err != nil {
		return "", fmt.Errorf("raw SetPreset failed: %w", err)
	}
	return parseRawSetPresetResponse(body)
}

// --- Raw SOAP XML response parsers ---

type ptzStatusResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName           xml.Name `xml:"Body"`
		GetStatusResponse struct {
			XMLName   xml.Name      `xml:"GetStatusResponse"`
			PTZStatus ptzStatusData `xml:"PTZStatus"`
		} `xml:"GetStatusResponse"`
	} `xml:"Body"`
}

type ptzStatusData struct {
	Position struct {
		PanTilt struct {
			X float64 `xml:"x,attr"`
			Y float64 `xml:"y,attr"`
		} `xml:"PanTilt"`
		Zoom struct {
			X float64 `xml:"x,attr"`
		} `xml:"Zoom"`
	} `xml:"Position"`
	MoveStatus struct {
		PanTilt string `xml:"PanTilt"`
		Zoom    string `xml:"Zoom"`
	} `xml:"MoveStatus"`
}

func parseRawGetStatusResponse(body []byte) (PTZVector, bool, error) {
	var resp ptzStatusResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return PTZVector{}, false, fmt.Errorf("parse GetStatus response: %w", err)
	}
	s := resp.Body.GetStatusResponse.PTZStatus
	pos := PTZVector{
		Pan:  s.Position.PanTilt.X,
		Tilt: s.Position.PanTilt.Y,
		Zoom: s.Position.Zoom.X,
	}
	moving := s.MoveStatus.PanTilt == "MOVING" || s.MoveStatus.Zoom == "MOVING"
	return pos, moving, nil
}

type setPresetResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName           xml.Name `xml:"Body"`
		SetPresetResponse struct {
			XMLName     xml.Name `xml:"SetPresetResponse"`
			PresetToken string   `xml:"PresetToken"`
		} `xml:"SetPresetResponse"`
	} `xml:"Body"`
}

func parseRawSetPresetResponse(body []byte) (string, error) {
	var resp setPresetResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse SetPreset response: %w", err)
	}
	return resp.Body.SetPresetResponse.PresetToken, nil
}
