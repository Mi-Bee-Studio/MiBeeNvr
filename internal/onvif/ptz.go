package onvif

import (
	"context"
	"fmt"
)

// PTZContinuousMove starts continuous PTZ movement.
func (c *Client) PTZContinuousMove(ctx context.Context, profileToken string, velocity PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go PTZ service
	return fmt.Errorf("PTZ continuous move not yet implemented")
}

// PTZAbsoluteMove moves to an absolute PTZ position.
func (c *Client) PTZAbsoluteMove(ctx context.Context, profileToken string, position PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go PTZ service
	return fmt.Errorf("PTZ absolute move not yet implemented")
}

// PTZRelativeMove moves by a relative PTZ displacement.
func (c *Client) PTZRelativeMove(ctx context.Context, profileToken string, displacement PTZVector) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go PTZ service
	return fmt.Errorf("PTZ relative move not yet implemented")
}

// PTZStop stops all PTZ movement.
func (c *Client) PTZStop(ctx context.Context, profileToken string) error {
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go PTZ service
	return fmt.Errorf("PTZ stop not yet implemented")
}

// PTZGetStatus returns the current PTZ position.
func (c *Client) PTZGetStatus(ctx context.Context, profileToken string) (*PTZVector, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go PTZ service
	return nil, fmt.Errorf("PTZ get status not yet implemented")
}
