package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- DeviceManagerImpl interface check ---

func TestDeviceManagerImpl_ImplementsInterface(t *testing.T) {
	t.Helper()
	var _ DeviceManager = (*DeviceManagerImpl)(nil)
}

func TestMockDeviceManager_ImplementsInterface(t *testing.T) {
	t.Helper()
	var _ DeviceManager = (*MockDeviceManager)(nil)
}

// --- MockDeviceManager tests ---

func TestMockDeviceManager_SystemReboot(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	err := m.SystemReboot(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, m.SystemRebootCalls)
}

func TestMockDeviceManager_SystemReboot_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	err := m.SystemReboot(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
	require.Equal(t, 1, m.SystemRebootCalls)
}

func TestMockDeviceManager_GetNetworkInterfaces(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{
		NetworkInterfaces: []NetworkInterface{
			{Name: "eth0", Enabled: true, IPv4: NetworkIPv4{Address: "192.168.1.100", Netmask: "255.255.255.0", DHCP: true}},
		},
	}
	ctx := context.Background()

	ifaces, err := m.GetNetworkInterfaces(ctx)
	require.NoError(t, err)
	require.Len(t, ifaces, 1)
	require.Equal(t, "eth0", ifaces[0].Name)
	require.True(t, ifaces[0].Enabled)
	require.Equal(t, "192.168.1.100", ifaces[0].IPv4.Address)
	require.Equal(t, 1, m.GetNetworkInterfacesCalls)
}

func TestMockDeviceManager_GetNetworkInterfaces_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	_, err := m.GetNetworkInterfaces(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockDeviceManager_SetNetworkInterfaces(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	err := m.SetNetworkInterfaces(ctx, []NetworkInterface{
		{Name: "eth0", IPv4: NetworkIPv4{Address: "10.0.0.1"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, m.SetNetworkInterfacesCalls)
}

func TestMockDeviceManager_SetNetworkInterfaces_Unsupported(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: ErrUnsupported}
	ctx := context.Background()

	err := m.SetNetworkInterfaces(ctx, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnsupported)
}

func TestMockDeviceManager_GetUsers(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{
		Users: []ONVIFUser{
			{Username: "admin", Level: "Administrator"},
			{Username: "operator", Level: "Operator"},
		},
	}
	ctx := context.Background()

	users, err := m.GetUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "admin", users[0].Username)
	require.Equal(t, "Administrator", users[0].Level)
	require.Equal(t, "operator", users[1].Username)
	require.Equal(t, 1, m.GetUsersCalls)
}

func TestMockDeviceManager_GetUsers_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	_, err := m.GetUsers(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockDeviceManager_CreateUsers(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	err := m.CreateUsers(ctx, []ONVIFUser{
		{Username: "newuser", Password: "pass123", Level: "User"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, m.CreateUsersCalls)
}

func TestMockDeviceManager_CreateUsers_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	err := m.CreateUsers(ctx, []ONVIFUser{{Username: "test"}})
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockDeviceManager_DeleteUsers(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	err := m.DeleteUsers(ctx, []string{"olduser"})
	require.NoError(t, err)
	require.Equal(t, 1, m.DeleteUsersCalls)
}

func TestMockDeviceManager_DeleteUsers_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	err := m.DeleteUsers(ctx, []string{"olduser"})
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockDeviceManager_SetUser(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	err := m.SetUser(ctx, "admin", "newpass")
	require.NoError(t, err)
	require.Equal(t, 1, m.SetUserCalls)
}

func TestMockDeviceManager_SetUser_Error(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{Error: errTestNotFound}
	ctx := context.Background()

	err := m.SetUser(ctx, "admin", "newpass")
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockDeviceManager_MultipleCalls(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	_ = m.SystemReboot(ctx)
	_, _ = m.GetNetworkInterfaces(ctx)
	_ = m.SetNetworkInterfaces(ctx, nil)
	_, _ = m.GetUsers(ctx)
	_ = m.CreateUsers(ctx, nil)
	_ = m.DeleteUsers(ctx, nil)
	_ = m.SetUser(ctx, "admin", "pass")

	require.Equal(t, 1, m.SystemRebootCalls)
	require.Equal(t, 1, m.GetNetworkInterfacesCalls)
	require.Equal(t, 1, m.SetNetworkInterfacesCalls)
	require.Equal(t, 1, m.GetUsersCalls)
	require.Equal(t, 1, m.CreateUsersCalls)
	require.Equal(t, 1, m.DeleteUsersCalls)
	require.Equal(t, 1, m.SetUserCalls)
}

// --- formatPrefixMask tests ---

func TestFormatPrefixMask_ValidPrefixes(t *testing.T) {
	t.Helper()
	tests := []struct {
		prefix int
		want   string
	}{
		{0, ""},
		{8, "ff000000"},
		{16, "ffff0000"},
		{24, "ffffff00"},
		{32, "ffffffff"},
		{12, "fff00000"},
		{20, "fffff000"},
		{28, "fffffff0"},
		{1, "80000000"},
		{31, "fffffffe"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			result := formatPrefixMask(tc.prefix)
			require.Equal(t, tc.want, result)
		})
	}
}

func TestFormatPrefixMask_InvalidPrefixes(t *testing.T) {
	t.Helper()
	require.Equal(t, "", formatPrefixMask(-1))
	require.Equal(t, "", formatPrefixMask(33))
	require.Equal(t, "", formatPrefixMask(100))
}

// --- ErrUnsupported ---

func TestErrUnsupported(t *testing.T) {
	t.Helper()
	require.Equal(t, "operation not supported", ErrUnsupported.Error())
}

// --- DeviceManagerImpl (direct tests) ---

func TestDeviceManagerImpl_SetNetworkInterfaces_ReturnsUnsupported(t *testing.T) {
	t.Helper()
	dm := NewDeviceManager(nil)
	err := dm.SetNetworkInterfaces(context.Background(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnsupported)
}

// --- NetworkInterface types ---

func TestNetworkInterface_ZeroValues(t *testing.T) {
	t.Helper()
	n := NetworkInterface{}
	require.Empty(t, n.Name)
	require.False(t, n.Enabled)
	require.Equal(t, NetworkIPv4{}, n.IPv4)
}

func TestONVIFUser_Fields(t *testing.T) {
	t.Helper()
	u := ONVIFUser{Username: "admin", Password: "secret", Level: "Administrator"}
	require.Equal(t, "admin", u.Username)
	require.Equal(t, "secret", u.Password)
	require.Equal(t, "Administrator", u.Level)
}

func TestNetworkIPv4_Fields(t *testing.T) {
	t.Helper()
	v4 := NetworkIPv4{Enabled: true, DHCP: true, Address: "192.168.1.1", Netmask: "255.255.255.0"}
	require.True(t, v4.Enabled)
	require.True(t, v4.DHCP)
	require.Equal(t, "192.168.1.1", v4.Address)
	require.Equal(t, "255.255.255.0", v4.Netmask)
}

func TestNetworkIPv6_Fields(t *testing.T) {
	t.Helper()
	v6 := NetworkIPv6{Enabled: true, DHCP: false, Address: "fe80::1", Prefix: 64}
	require.True(t, v6.Enabled)
	require.False(t, v6.DHCP)
	require.Equal(t, "fe80::1", v6.Address)
	require.Equal(t, 64, v6.Prefix)
}

// Test timing for concurrent device management calls
func TestMockDeviceManager_ConcurrentAccess(t *testing.T) {
	t.Helper()
	m := &MockDeviceManager{}
	ctx := context.Background()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = m.SystemReboot(ctx)
			_, _ = m.GetNetworkInterfaces(ctx)
			_, _ = m.GetUsers(ctx)
		}()
	}

	// Wait for all goroutines
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("timed out waiting for goroutines")
		}
	}
	require.Equal(t, 10, m.SystemRebootCalls)
	require.Equal(t, 10, m.GetNetworkInterfacesCalls)
	require.Equal(t, 10, m.GetUsersCalls)
}
