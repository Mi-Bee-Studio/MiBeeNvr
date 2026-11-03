package camera

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// GB28181 alarm linkage for thermal channels (dual-spectrum): a device-side
// temperature alarm (fire-point / over-threshold NOTIFY) drives the same
// trigger surface the ONVIF RadiometryAlarm path uses — linked recording
// plus one evidence snapshot, through the shared dispatcher.
//
// Scoped to imaging_type=thermal cameras so existing GB cameras keep their
// current behavior: their motion alarms stay display-only (the library's
// alarm→stream-hold linkage remains available for them via config), and a
// temperature alarm on a thermal channel is exactly the event whose moment
// the user will want on disk. GB alarm NOTIFYs carry no true/clear pairing
// — each notification is one shot; repeated alarms re-trigger (StartCamera
// reports already-running, which the dispatcher logs and tolerates).

// consumeGBAlarms subscribes to the NVR bus's GB alarm topic and applies
// the thermal linkage. Runs for the manager's lifetime when constructed
// with an event bus; each test/prod manager instance owns its loop.
func (cm *CameraManager) consumeGBAlarms() {
	ch := make(chan event.Event, 16)
	if err := cm.eventBus.Subscribe(event.TopicGB28181Alarm, ch, 16); err != nil {
		logger.Warn("thermal gb alarm linkage not armed", "error", err)
		return
	}
	for evt := range ch {
		alarm, ok := evt.Data.(event.GB28181AlarmEvent)
		if !ok {
			continue
		}
		cm.handleThermalGBAlarm(alarm)
	}
}

// handleThermalGBAlarm applies the linkage policy for one device alarm:
// thermal camera → record + evidence snapshot (trigger-labelled gb);
// anything else → display-only, unchanged.
func (cm *CameraManager) handleThermalGBAlarm(alarm event.GB28181AlarmEvent) {
	if alarm.CameraID == "" {
		// Un-enrolled channel (the alarm still flows to SSE/REST for display).
		return
	}
	cam := cm.snapshotConfig(alarm.CameraID)
	if cam == nil || cam.ImagingType != config.ImagingTypeThermal {
		return
	}
	logger.Info("thermal gb alarm linkage",
		"camera_id", alarm.CameraID,
		"device", alarm.DeviceID, "channel", alarm.ChannelID,
		"method", alarm.AlarmMethod, "type", alarm.AlarmType, "priority", alarm.AlarmPriority)
	if cm.motionAction != nil {
		cm.motionAction(alarm.CameraID, "record", 0)
		cm.motionAction(alarm.CameraID, "snapshot:gb", 0)
	}
}

// armThermalGBAlarmLinkage starts the consumer goroutine; nil bus = no-op.
// Called from NewCameraManager when an event bus was provided.
func (cm *CameraManager) armThermalGBAlarmLinkage() {
	if cm.eventBus == nil {
		return
	}
	go cm.consumeGBAlarms()
}
