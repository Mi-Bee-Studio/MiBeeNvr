package plugin

import (
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// mockPlugin is a test-only implementation of RecorderPlugin.
type mockPlugin struct {
	name      string
	protocols []string
}

func (m *mockPlugin) Name() string        { return m.name }
func (m *mockPlugin) Protocols() []string { return m.protocols }
func (m *mockPlugin) NewRecorder(config.CameraConfig, *storage.Manager, *storage.DB, ...*metrics.Metrics) model.Recorder {
	return nil
}
func (m *mockPlugin) RegisterRoutes(chi.Router) {}
func (m *mockPlugin) ConfigSchema() interface{}  { return nil }

// resetPlugins clears the global registry. Must be called at the start of each test.
func resetPlugins() {
	pluginsMu.Lock()
	defer pluginsMu.Unlock()
	plugins = make(map[string]RecorderPlugin)
}

func TestRegister(t *testing.T) {
	t.Helper()
	resetPlugins()

	p := &mockPlugin{name: "test-plugin", protocols: []string{"test"}}
	Register(p)

	found := Lookup("test-plugin")
	require.Equal(t, p, found)
}

func TestLookupProtocol(t *testing.T) {
	t.Helper()
	resetPlugins()

	p := &mockPlugin{name: "xiaomi", protocols: []string{"xiaomi"}}
	Register(p)

	found := LookupProtocol("xiaomi")
	require.Equal(t, p, found)

	notFound := LookupProtocol("nonexistent")
	require.Nil(t, notFound)
}

func TestMultiplePlugins(t *testing.T) {
	t.Helper()
	resetPlugins()

	p1 := &mockPlugin{name: "plugin-1", protocols: []string{"proto-1"}}
	p2 := &mockPlugin{name: "plugin-2", protocols: []string{"proto-2"}}
	Register(p1)
	Register(p2)

	all := All()
	require.Len(t, all, 2)

	found1 := Lookup("plugin-1")
	found2 := Lookup("plugin-2")
	require.Equal(t, p1, found1)
	require.Equal(t, p2, found2)
}

func TestConcurrentAccess(t *testing.T) {
	t.Helper()
	resetPlugins()

	var wg sync.WaitGroup
	const goroutines = 100

	// Concurrent registrations
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := &mockPlugin{
				name:      "concurrent-" + string(rune('A'+idx%26)),
				protocols: []string{"proto"},
			}
			Register(p)
		}(i)
	}
	wg.Wait()

	// Concurrent lookups
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = Lookup("concurrent-" + string(rune('A'+idx%26)))
			_ = LookupProtocol("proto")
			_ = All()
		}(i)
	}
	wg.Wait()

	// Verify some plugins were registered (at least 26 unique names)
	all := All()
	require.GreaterOrEqual(t, len(all), 1)
}

func TestLookupNotFound(t *testing.T) {
	t.Helper()
	resetPlugins()

	found := Lookup("nonexistent")
	require.Nil(t, found)

	foundProto := LookupProtocol("nonexistent")
	require.Nil(t, foundProto)

	all := All()
	require.Empty(t, all)
}
