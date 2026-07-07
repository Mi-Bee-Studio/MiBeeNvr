package camera

// SetRelayManager wires the relay engine (optional). When set, the camera
// manager reconciles a camera's push-out targets on Add/Update/Remove.
func (cm *CameraManager) SetRelayManager(rm RelayManager) {
	cm.relayMgr = rm
}

// RelayStatus returns the runtime status of a camera's push-out targets, for the
// push-status API and the camera card UI. Empty when no relay manager is wired.
// RelayStatusProvider is the minimal shape the API handler needs.
func (cm *CameraManager) RelayStatus(cameraID string) []any {
	if rm, ok := cm.relayMgr.(relayStatusProvider); ok {
		return rm.CameraStatusJSON(cameraID)
	}
	return nil
}

// relayStatusProvider is implemented by *relay.Manager to return JSON-serializable
// status without internal/camera importing internal/relay.
type relayStatusProvider interface {
	CameraStatusJSON(cameraID string) []any
}
