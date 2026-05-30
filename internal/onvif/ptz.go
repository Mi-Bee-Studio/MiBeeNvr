package onvif

import (
	"context"
	"fmt"
	"sync"

	onvifgo "github.com/0x524a/onvif-go"
)

// PTZControllerImpl implements PTZController by delegating to onvif-go's PTZ service.
// It wraps an onvif-go Client and stores the profile token internally.
type PTZControllerImpl struct {
	client       *onvifgo.Client
	profileToken string
	mu           sync.Mutex
}

// NewPTZController creates a PTZController backed by an onvif-go client.
func NewPTZController(client *onvifgo.Client, profileToken string) *PTZControllerImpl {
	return &PTZControllerImpl{
		client:       client,
		profileToken: profileToken,
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

	return p.client.ContinuousMove(ctx, p.profileToken, toOnvifPTZSpeed(velocity), nil)
}

// AbsoluteMove moves PTZ to an absolute position.
func (p *PTZControllerImpl) AbsoluteMove(ctx context.Context, position PTZVector) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.AbsoluteMove(ctx, p.profileToken, toOnvifPTZVector(position), nil)
}

// RelativeMove moves PTZ relative to the current position.
func (p *PTZControllerImpl) RelativeMove(ctx context.Context, displacement PTZVector) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.RelativeMove(ctx, p.profileToken, toOnvifPTZVector(displacement), nil)
}

// Stop stops PTZ movement. stopPanTilt and stopZoom control which axes to stop.
func (p *PTZControllerImpl) Stop(ctx context.Context, stopPanTilt, stopZoom bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.Stop(ctx, p.profileToken, stopPanTilt, stopZoom)
}

// GetStatus returns the current PTZ position and whether the camera is moving.
func (p *PTZControllerImpl) GetStatus(ctx context.Context) (position PTZVector, moving bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status, err := p.client.GetStatus(ctx, p.profileToken)
	if err != nil {
		return PTZVector{}, false, fmt.Errorf("get PTZ status failed: %w", err)
	}
	return fromOnvifPTZStatus(status)
}

// GetPresets returns all PTZ presets on the camera.
func (p *PTZControllerImpl) GetPresets(ctx context.Context) ([]PTZPreset, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	presets, err := p.client.GetPresets(ctx, p.profileToken)
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
func (p *PTZControllerImpl) SetPreset(ctx context.Context, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	token, err := p.client.SetPreset(ctx, p.profileToken, name, "")
	if err != nil {
		return "", fmt.Errorf("set PTZ preset failed: %w", err)
	}
	return token, nil
}

// GoToPreset moves the camera to a saved preset position.
func (p *PTZControllerImpl) GoToPreset(ctx context.Context, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.GotoPreset(ctx, p.profileToken, token, nil)
}

// RemovePreset deletes a PTZ preset.
func (p *PTZControllerImpl) RemovePreset(ctx context.Context, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client.RemovePreset(ctx, p.profileToken, token)
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
