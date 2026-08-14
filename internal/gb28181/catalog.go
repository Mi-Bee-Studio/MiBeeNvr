package gb28181

import (
	"fmt"
	"sync/atomic"
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
	sn := c.seq.Add(1)
	// GB/T 28181-2016 § 9.3.1: platform-to-device Query uses child elements
	// for CmdType/SN/DeviceID (NOT attributes). Many device parsers reject
	// the attribute form even though some emitters (and our Decode probe)
	// accept it.
	body := []byte(fmt.Sprintf(`<Query><CmdType>Catalog</CmdType><SN>%d</SN><DeviceID>%s</DeviceID></Query>`, sn, deviceID))
	if err := c.sender.SendMessage(deviceID, body); err != nil {
		return fmt.Errorf("gb28181: send Catalog query to %s: %w", deviceID, err)
	}
	return nil
}
