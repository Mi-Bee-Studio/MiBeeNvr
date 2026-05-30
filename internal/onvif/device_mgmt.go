package onvif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	onvifgo "github.com/0x524a/onvif-go"
)

// ErrUnsupported indicates the operation is not supported.
var ErrUnsupported = errors.New("operation not supported")
// deviceMgmtLogger is used for device management operations.
var deviceMgmtLogger = slog.Default().With("component", "onvif-device-mgmt")

// DeviceManagerImpl implements DeviceManager by delegating to onvif-go's device service.
type DeviceManagerImpl struct {
	client *onvifgo.Client
	mu     sync.Mutex
}

// Compile-time interface check.
var _ DeviceManager = (*DeviceManagerImpl)(nil)

// NewDeviceManager creates a DeviceManager backed by an onvif-go client.
func NewDeviceManager(client *onvifgo.Client) *DeviceManagerImpl {
	return &DeviceManagerImpl{client: client}
}

// SystemReboot reboots the ONVIF device.
func (d *DeviceManagerImpl) SystemReboot(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	message, err := d.client.SystemReboot(ctx)
	if err != nil {
		return fmt.Errorf("system reboot failed: %w", err)
	}

	deviceMgmtLogger.Info("device rebooted", "message", message)
	return nil
}

// GetNetworkInterfaces retrieves network interface configuration from the device.
func (d *DeviceManagerImpl) GetNetworkInterfaces(ctx context.Context) ([]NetworkInterface, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ifaces, err := d.client.GetNetworkInterfaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("get network interfaces failed: %w", err)
	}

	result := make([]NetworkInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		n := NetworkInterface{
			Name:    iface.Info.Name,
			Enabled: iface.Enabled,
		}
		if iface.IPv4 != nil {
			n.IPv4 = NetworkIPv4{
				Enabled: iface.IPv4.Enabled,
			}
			for _, addr := range iface.IPv4.Config.Manual {
				if addr.Address != "" {
					n.IPv4.Address = addr.Address
					n.IPv4.Netmask = formatPrefixMask(addr.PrefixLength)
					break
				}
			}
			if iface.IPv4.Config.DHCP {
				n.IPv4.DHCP = true
			}
		}
		if iface.IPv6 != nil {
			n.IPv6 = NetworkIPv6{
				Enabled: iface.IPv6.Enabled,
			}
			if iface.IPv6.Config.DHCP {
				n.IPv6.DHCP = true
			}
		}
		result = append(result, n)
	}
	return result, nil
}

// SetNetworkInterfaces configures network interfaces on the device.
// Note: onvif-go does not expose SetNetworkInterfaces as a high-level method.
func (d *DeviceManagerImpl) SetNetworkInterfaces(ctx context.Context, interfaces []NetworkInterface) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_ = ctx
	_ = interfaces
	return fmt.Errorf("set network interfaces: %w", ErrUnsupported)
}

// GetUsers retrieves user accounts from the ONVIF device.
func (d *DeviceManagerImpl) GetUsers(ctx context.Context) ([]ONVIFUser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	users, err := d.client.GetUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("get users failed: %w", err)
	}

	result := make([]ONVIFUser, 0, len(users))
	for _, u := range users {
		result = append(result, ONVIFUser{
			Username: u.Username,
			Level:    u.UserLevel,
		})
	}
	return result, nil
}

// CreateUsers creates new user accounts on the ONVIF device.
func (d *DeviceManagerImpl) CreateUsers(ctx context.Context, users []ONVIFUser) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	onvifUsers := make([]*onvifgo.User, len(users))
	for i, u := range users {
		onvifUsers[i] = &onvifgo.User{
			Username:  u.Username,
			Password:  u.Password,
			UserLevel: u.Level,
		}
	}

	if err := d.client.CreateUsers(ctx, onvifUsers); err != nil {
		return fmt.Errorf("create users failed: %w", err)
	}

	deviceMgmtLogger.Info("users created", "count", len(users))
	return nil
}

// DeleteUsers deletes user accounts from the ONVIF device.
func (d *DeviceManagerImpl) DeleteUsers(ctx context.Context, usernames []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.client.DeleteUsers(ctx, usernames); err != nil {
		return fmt.Errorf("delete users failed: %w", err)
	}

	deviceMgmtLogger.Info("users deleted", "count", len(usernames))
	return nil
}

// SetUser modifies an existing user account on the ONVIF device.
func (d *DeviceManagerImpl) SetUser(ctx context.Context, username, password string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.client.SetUser(ctx, &onvifgo.User{
		Username: username,
		Password: password,
	}); err != nil {
		return fmt.Errorf("set user failed: %w", err)
	}

	deviceMgmtLogger.Info("user updated", "username", username)
	return nil
}

// formatPrefixMask converts a CIDR prefix length to a dotted netmask string.
func formatPrefixMask(prefixLength int) string {
	if prefixLength <= 0 || prefixLength > 32 {
		return ""
	}
	var mask = net.IPv4Mask(0, 0, 0, 0)
	for i := 0; i < 4; i++ {
		bits := prefixLength - i*8
		if bits >= 8 {
			mask[i] = 0xFF
		} else if bits > 0 {
			mask[i] = ^byte(0xFF >> bits)
		}
	}
	return mask.String()
}