package mqtt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// mockCallback tracks calls to the onAction callback.
type mockCallback struct {
	cameraID string
	action   string
	called   bool
}

func (m *mockCallback) callback(cameraID, action string) {
	m.called = true
	m.cameraID = cameraID
	m.action = action
}

// mockMessage implements mqtt.Message for testing.
type mockMessage struct {
	topic   string
	payload []byte
}

func (m *mockMessage) Duplicate() bool                          { return false }
func (m *mockMessage) Qos() byte                                { return 1 }
func (m *mockMessage) Retained() bool                           { return false }
func (m *mockMessage) Topic() string                            { return m.topic }
func (m *mockMessage) MessageID() uint16                        { return 0 }
func (m *mockMessage) Payload() []byte                          { return m.payload }
func (m *mockMessage) Ack()                                     {}

func TestNewClient(t *testing.T) {
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test-client", "mibee-nvr", cb.callback)

	assert.Equal(t, "tcp://localhost:1883", c.brokerURL)
	assert.Equal(t, "test-client", c.clientID)
	assert.Equal(t, "mibee-nvr", c.topicPrefix)
	assert.NotNil(t, c.onAction)
}

func TestParseActionStart(t *testing.T) {
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", cb.callback)

	msg := &mockMessage{
		topic:   "mibee-nvr/trigger/camera1",
		payload: []byte(`{"action": "start"}`),
	}

	c.handleMessage(nil, msg)

	assert.True(t, cb.called)
	assert.Equal(t, "camera1", cb.cameraID)
	assert.Equal(t, "start", cb.action)
}

func TestParseActionStop(t *testing.T) {
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", cb.callback)

	msg := &mockMessage{
		topic:   "mibee-nvr/trigger/camera2",
		payload: []byte(`{"action": "stop"}`),
	}

	c.handleMessage(nil, msg)

	assert.True(t, cb.called)
	assert.Equal(t, "camera2", cb.cameraID)
	assert.Equal(t, "stop", cb.action)
}

func TestIsConfigured(t *testing.T) {
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", nil)
	assert.True(t, c.IsConfigured())
}

func TestNotConfiguredNoOp(t *testing.T) {
	c := NewClient("", "test", "mibee-nvr", nil)
	assert.False(t, c.IsConfigured())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so Start returns
	err := c.Start(ctx)
	assert.NoError(t, err)
}

// Ensure mockMessage satisfies mqtt.Message interface at compile time.
var _ mqtt.Message = (*mockMessage)(nil)
