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

// --- Client PTZ stubs (legacy, to be wired in T13) ---

// PTZContinuousMove starts continuous PTZ movement.
func (c *Client) PTZContinuousMove(ctx context.Context, profileToken string, velocity PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	return fmt.Errorf("PTZ continuous move not yet implemented")
}

// PTZAbsoluteMove moves to an absolute PTZ position.
func (c *Client) PTZAbsoluteMove(ctx context.Context, profileToken string, position PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	return fmt.Errorf("PTZ absolute move not yet implemented")
}

// PTZRelativeMove moves by a relative PTZ displacement.
func (c *Client) PTZRelativeMove(ctx context.Context, profileToken string, displacement PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	return fmt.Errorf("PTZ relative move not yet implemented")
}

// PTZStop stops all PTZ movement.
func (c *Client) PTZStop(ctx context.Context, profileToken string) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	return fmt.Errorf("PTZ stop not yet implemented")
}

// PTZGetStatus returns the current PTZ position.
func (c *Client) PTZGetStatus(ctx context.Context, profileToken string) (*PTZVector, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	return nil, fmt.Errorf("PTZ get status not yet implemented")
}
