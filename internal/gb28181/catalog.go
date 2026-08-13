package gb28181

import (
	"fmt"
	"sync/atomic"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
)

// CatalogController sends platform-to-device Catalog queries over the SIP
// MESSAGE transport. When a device receives a Catalog query it responds with
// its channel list, which the SIP MESSAGE handler parses and registers via
// DeviceManager.RegisterChannel + db.UpsertGB28181Channel.
type CatalogController struct {
	devices *DeviceManager
	sender  MessageSender
	seq     atomic.Int64 // MANSCDP SN sequence
}

// NewCatalogController creates a controller sending through sender.
func NewCatalogController(devices *DeviceManager, sender MessageSender) *CatalogController {
	return &CatalogController{devices: devices, sender: sender}
}

// RequestCatalog sends a MANSCDP Catalog query to deviceID. The device must be
// registered and online; the catalog response arrives asynchronously as a
// later SIP MESSAGE (handled by the SIP server's handleMessage → Decode →
// RegisterChannel + db.UpsertGB28181Channel).
func (c *CatalogController) RequestCatalog(deviceID string) error {
	dev, ok := c.devices.Device(deviceID)
	if !ok {
		return ErrDeviceOffline
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	body, err := manscdp.Encode(manscdp.CatalogQuery{
		CmdType:  manscdp.CmdCatalog,
		SN:       int(c.seq.Add(1)),
		DeviceID: deviceID,
	})
	if err != nil {
		return fmt.Errorf("gb28181: encode Catalog query: %w", err)
	}
	if err := c.sender.SendMessage(deviceID, body); err != nil {
		return fmt.Errorf("gb28181: send Catalog query to %s: %w", deviceID, err)
	}
	return nil
}
