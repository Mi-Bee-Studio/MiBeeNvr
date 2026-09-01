package mqtt

import (
	"context"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
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

func (m *mockMessage) Duplicate() bool   { return false }
func (m *mockMessage) Qos() byte         { return 1 }
func (m *mockMessage) Retained() bool    { return false }
func (m *mockMessage) Topic() string     { return m.topic }
func (m *mockMessage) MessageID() uint16 { return 0 }
func (m *mockMessage) Payload() []byte   { return m.payload }
func (m *mockMessage) Ack()              {}

func TestNewClient(t *testing.T) {
	t.Helper()
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test-client", "mibee-nvr", "", "", cb.callback)

	assert.Equal(t, "tcp://localhost:1883", c.brokerURL)
	assert.Equal(t, "test-client", c.clientID)
	assert.Equal(t, "mibee-nvr", c.topicPrefix)
	assert.Equal(t, "", c.username)
	assert.Equal(t, "", c.password)
	assert.NotNil(t, c.onAction)
}

func TestParseActionStart(t *testing.T) {
	t.Helper()
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", "", "", cb.callback)

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
	t.Helper()
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", "", "", cb.callback)

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
	t.Helper()
	c := NewClient("tcp://localhost:1883", "test", "mibee-nvr", "", "", nil)
	assert.True(t, c.IsConfigured())
}

func TestHasActionHandler(t *testing.T) {
	t.Helper()
	cb := &mockCallback{}
	withHandler := NewClient("tcp://localhost:1883", "test", "mibee-nvr", "", "", cb.callback)
	assert.True(t, withHandler.HasActionHandler())

	withoutHandler := NewClient("tcp://localhost:1883", "test", "mibee-nvr", "", "", nil)
	assert.False(t, withoutHandler.HasActionHandler())
}

func TestNotConfiguredNoOp(t *testing.T) {
	t.Helper()
	c := NewClient("", "test", "mibee-nvr", "", "", nil)
	assert.False(t, c.IsConfigured())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so Start returns
	err := c.Start(ctx)
	assert.NoError(t, err)
}

// Ensure mockMessage satisfies mqtt.Message interface at compile time.

func TestNewClientWithAuth(t *testing.T) {
	t.Helper()
	cb := &mockCallback{}
	c := NewClient("tcp://localhost:1883", "test-client", "mibee-nvr", "mqtt-user", "mqtt-pass", cb.callback)

	assert.Equal(t, "mqtt-user", c.username)
	assert.Equal(t, "mqtt-pass", c.password)
	assert.True(t, c.IsConfigured())
}

var _ mqtt.Message = (*mockMessage)(nil)

// --- Publish tests ---

// mockToken implements mqtt.Token for testing.
type mockToken struct {
	err error
}

func (t *mockToken) Wait() bool                       { return true }
func (t *mockToken) WaitTimeout(d time.Duration) bool { return true }
func (t *mockToken) Done() <-chan struct{}            { return nil }
func (t *mockToken) Error() error                     { return t.err }

// mockPahoClient implements mqtt.Client for testing.
type mockPahoClient struct {
	connected    bool
	publishToken mqtt.Token
}

func (m *mockPahoClient) IsConnected() bool       { return m.connected }
func (m *mockPahoClient) IsConnectionOpen() bool  { return m.connected }
func (m *mockPahoClient) Connect() mqtt.Token     { return nil }
func (m *mockPahoClient) Disconnect(quiesce uint) {}
func (m *mockPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	return m.publishToken
}

func (m *mockPahoClient) Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token {
	return nil
}

func (m *mockPahoClient) SubscribeMultiple(filters map[string]byte, callback mqtt.MessageHandler) mqtt.Token {
	return nil
}
func (m *mockPahoClient) Unsubscribe(topics ...string) mqtt.Token             { return nil }
func (m *mockPahoClient) AddRoute(topic string, callback mqtt.MessageHandler) {}
func (m *mockPahoClient) OptionsReader() mqtt.ClientOptionsReader             { return mqtt.ClientOptionsReader{} }

func TestPublish_NilClient(t *testing.T) {
	t.Helper()
	var c *Client
	err := c.Publish("test/topic", "hello")
	assert.ErrorContains(t, err, "not connected")
}

func TestPublish_NilMQTTClient(t *testing.T) {
	t.Helper()
	c := &Client{topicPrefix: "test"}
	err := c.Publish("test/topic", "hello")
	assert.ErrorContains(t, err, "not connected")
}

func TestPublish_Disconnected(t *testing.T) {
	t.Helper()
	mockClient := &mockPahoClient{connected: false}
	c := &Client{topicPrefix: "test", mqttClient: mockClient}
	err := c.Publish("test/topic", "hello")
	assert.ErrorContains(t, err, "not connected")
}

func TestPublish_MarshalError(t *testing.T) {
	t.Helper()
	mockClient := &mockPahoClient{connected: true}
	c := &Client{topicPrefix: "test", mqttClient: mockClient}
	// channel can't be marshaled to JSON
	err := c.Publish("test/topic", make(chan int))
	assert.ErrorContains(t, err, "marshal")
}

func TestPublish_PublishError(t *testing.T) {
	t.Helper()
	mockClient := &mockPahoClient{
		connected:    true,
		publishToken: &mockToken{err: assert.AnError},
	}
	c := &Client{topicPrefix: "test", mqttClient: mockClient}
	err := c.Publish("test/topic", "hello")
	assert.ErrorContains(t, err, "mqtt publish")
}

func TestPublish_Success(t *testing.T) {
	t.Helper()
	mockClient := &mockPahoClient{
		connected:    true,
		publishToken: &mockToken{},
	}
	c := &Client{topicPrefix: "test", mqttClient: mockClient}
	err := c.Publish("test/topic", map[string]string{"key": "value"})
	assert.NoError(t, err)
}

var _ mqtt.Client = (*mockPahoClient)(nil)
