package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// --- Unit tests (no real plugin processes) ---

func TestNewPluginManager(t *testing.T) {
	t.Helper()
	cfg := &config.PluginsConfig{
		Directory: "/tmp/test-plugins",
		Plugins:   make(map[string]config.PluginEntryConfig),
	}
	m := NewPluginManager(cfg)
	require.NotNil(t, m)
	require.Empty(t, m.ListPlugins())
}

func TestPluginManagerGetPluginEmpty(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	mp, ok := m.GetPlugin("nonexistent")
	require.False(t, ok)
	require.Nil(t, mp)
}

func TestPluginManagerGetClientEmpty(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	client, ok := m.GetClient("nonexistent")
	require.False(t, ok)
	require.Nil(t, client)
}

func TestPluginManagerListPluginsEmpty(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	list := m.ListPlugins()
	require.Empty(t, list)
}

func TestPluginManagerStopNoPlugins(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	// Stop with no plugins should not panic
	m.Stop()
}

func TestPluginManagerStartNoEnabled(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Directory: "/tmp/test-plugins",
		Plugins: map[string]config.PluginEntryConfig{
			"test": {Enabled: false, Path: "/nonexistent/binary"},
		},
	})

	err := m.Start(context.Background())
	require.NoError(t, err)
	m.Stop()
}

func TestPluginManagerStartMissingBinary(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Directory: "/tmp/test-plugins-nonexistent",
		Plugins: map[string]config.PluginEntryConfig{
			"missing": {Enabled: true, Path: "/nonexistent/plugin-binary"},
		},
	})

	err := m.Start(context.Background())
	// Should not error — individual plugin failures are logged, not returned
	require.NoError(t, err)
	require.Empty(t, m.ListPlugins())
	m.Stop()
}

func TestResolvePluginPath(t *testing.T) {
	t.Helper()
	tests := []struct {
		name      string
		entryPath string
		dir       string
		plugin    string
		want      string
	}{
		{"explicit path", "/usr/local/bin/my-plugin", "/plugins", "test", "/usr/local/bin/my-plugin"},
		{"empty path uses dir", "", "/opt/plugins", "xiaomi", "/opt/plugins/xiaomi"},
		{"relative dir", "", "./plugins", "demo", "plugins/demo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			got := resolvePluginPath(tc.entryPath, tc.dir, tc.plugin)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestPluginManagerInterface(t *testing.T) {
	t.Helper()
	// Verify PluginInterface implements the go-plugin Plugin interface
	// by calling its methods directly (no subprocess needed)
	p := &PluginInterface{}

	// GRPCClient should return a PluginServiceClient
	result, err := p.GRPCClient(context.Background(), nil, nil)
	// nil ClientConn will cause NewPluginServiceClient to return a valid but unusable client
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestPluginStatusConstants(t *testing.T) {
	t.Helper()
	require.Equal(t, PluginStatus("running"), StatusRunning)
	require.Equal(t, PluginStatus("stopped"), StatusStopped)
	require.Equal(t, PluginStatus("error"), StatusError)
}

func TestHandshakeConfig(t *testing.T) {
	t.Helper()
	require.Equal(t, "NVR_PLUGIN", MagicCookieKey)
	require.Equal(t, "mibee-nvr-plugin", MagicCookieValue)
	require.Equal(t, uint(1), Handshake.ProtocolVersion)
}

func TestPluginConstants(t *testing.T) {
	t.Helper()
	require.Equal(t, 30*time.Second, DefaultHealthCheckInterval)
	require.Equal(t, 1*time.Second, DefaultInitBackoff)
	require.Equal(t, 60*time.Second, DefaultMaxBackoff)
	require.Equal(t, 10, MaxRestartAttempts)
	require.Equal(t, "nvr_plugin", PluginType)
}

func TestPluginMapReturnsExpectedType(t *testing.T) {
	t.Helper()
	pm := pluginMap()
	_, ok := pm[PluginType]
	require.True(t, ok, "plugin map should contain PluginType key")
}

// --- Registry integration with plugin manager ---

func TestPluginManagerWithInProcessRegistry(t *testing.T) {
	t.Helper()
	// Verify that the existing in-process plugin registry still works
	// alongside the new gRPC manager
	resetPlugins()

	p := &mockPlugin{name: "in-process-test", protocols: []string{"test"}}
	Register(p)

	found := Lookup("in-process-test")
	require.Equal(t, p, found)

	// gRPC manager should coexist
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})
	require.NotNil(t, m)
	require.Empty(t, m.ListPlugins())
}

func TestPluginManagerConcurrentAccess(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	// Manually inject a ManagedPlugin for concurrency testing
	m.mu.Lock()
	m.plugins["concurrent-test"] = &ManagedPlugin{
		Name:   "concurrent-test",
		Status: StatusRunning,
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	const goroutines = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.GetPlugin("concurrent-test")
			_, _ = m.GetClient("concurrent-test")
			_ = m.ListPlugins()
		}()
	}
	wg.Wait()

	// Verify state intact
	mp, ok := m.GetPlugin("concurrent-test")
	require.True(t, ok)
	require.Equal(t, StatusRunning, mp.Status)
}

func TestPluginManagerManagedPluginStatusTracking(t *testing.T) {
	t.Helper()
	m := NewPluginManager(&config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	})

	// Simulate status transitions
	mp := &ManagedPlugin{
		Name:   "status-test",
		Status: StatusRunning,
	}

	m.mu.Lock()
	m.plugins["status-test"] = mp
	m.mu.Unlock()

	got, ok := m.GetPlugin("status-test")
	require.True(t, ok)
	require.Equal(t, StatusRunning, got.Status)

	// Simulate error state
	m.mu.Lock()
	m.plugins["status-test"].Status = StatusError
	m.mu.Unlock()

	got, ok = m.GetPlugin("status-test")
	require.True(t, ok)
	require.Equal(t, StatusError, got.Status)
}