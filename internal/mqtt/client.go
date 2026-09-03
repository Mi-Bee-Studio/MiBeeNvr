package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
)

var clientLogger = slogx.Component("mqtt")

// triggerMessage is the JSON payload of a camera trigger event.
type triggerMessage struct {
	Action string `json:"action"`
}

// aiTriggerMessage is the JSON payload of an AI detection event.
type aiTriggerMessage struct {
	CameraID   string           `json:"camera_id"`
	Event      string           `json:"event"`
	Timestamp  string           `json:"timestamp"`
	Detections []AiDetectionObj `json:"detections,omitempty"`
}

type AiDetectionObj struct {
	Label      string     `json:"label"`
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
}

// Client subscribes to MQTT topics for camera trigger events.
type Client struct {
	brokerURL   string
	clientID    string
	topicPrefix string
	username    string
	password    string
	mu          sync.Mutex
	mqttClient  mqtt.Client
	onAction    func(cameraID string, action string)
	// connectFn creates the paho client; overridable in tests. Nil = mqtt.NewClient.
	connectFn func(opts *mqtt.ClientOptions) mqtt.Client
}

// NewClient creates a new MQTT trigger event subscriber.
func NewClient(brokerURL, clientID, topicPrefix, username, password string, onAction func(cameraID, action string)) *Client {
	return &Client{
		brokerURL:   brokerURL,
		clientID:    clientID,
		topicPrefix: topicPrefix,
		username:    username,
		password:    password,
		onAction:    onAction,
	}
}

// IsConfigured returns true if the broker URL is non-empty.
func (c *Client) IsConfigured() bool {
	return c.brokerURL != ""
}

// setMQTTClient / getMQTTClient guard the paho handle: Start writes it from
// its service goroutine while Publish/Stop read it from arbitrary goroutines
// (health pipeline, status publisher) — surfaced by the race detector under
// the connect-retry loop (#661).
func (c *Client) setMQTTClient(client mqtt.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mqttClient = client
}

func (c *Client) getMQTTClient() mqtt.Client {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mqttClient
}

// HasActionHandler reports whether trigger messages are dispatched to an
// action callback. Production wiring must pass a non-nil onAction; this lets
// assembly tests assert the trigger path is actually connected.
func (c *Client) HasActionHandler() bool {
	return c != nil && c.onAction != nil
}

// Start connects to the MQTT broker and subscribes to trigger events.
// It blocks until ctx is cancelled. If MQTT is not configured, it returns immediately.
//
// A broker that is unreachable at startup does not fail Start (#661): the
// connect is retried with tiered backoff until it succeeds or ctx is done
// (deployments must not depend on broker/NVR start order). On connect,
// SetAutoReconnect + the OnConnect handler keep the trigger subscription
// alive across broker restarts.
func (c *Client) Start(ctx context.Context) error {
	if !c.IsConfigured() {
		return nil
	}

	opts := mqtt.NewClientOptions().
		AddBroker(c.brokerURL).
		SetClientID(c.clientID).
		SetAutoReconnect(true).
		SetOnConnectHandler(func(client mqtt.Client) {
			topic := c.topicPrefix + "/trigger/+"
			token := client.Subscribe(topic, 1, c.handleMessage)
			token.Wait()
		})

	if c.username != "" {
		opts.SetUsername(c.username)
		if c.password != "" {
			opts.SetPassword(c.password)
		}
	}

	connect := c.connectFn
	if connect == nil {
		connect = mqtt.NewClient
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		client := connect(opts)
		token := client.Connect()
		token.Wait()
		err := token.Error()
		if err == nil {
			c.setMQTTClient(client)
			break
		}
		lastErr = err
		delay := backoff.TieredBackoff(attempt)
		clientLogger.Warn("mqtt broker unreachable, retrying",
			"broker", c.brokerURL, "attempt", attempt+1, "error", lastErr, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return fmt.Errorf("mqtt: connect abandoned (context done), last error: %w", lastErr)
		case <-time.After(delay):
		}
	}

	<-ctx.Done()
	return nil
}

// Stop disconnects gracefully from the MQTT broker.
func (c *Client) Stop() error {
	if client := c.getMQTTClient(); client != nil && client.IsConnected() {
		client.Disconnect(1000)
	}
	return nil
}

// handleMessage parses incoming MQTT messages and calls the onAction callback.
func (c *Client) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	prefix := c.topicPrefix + "/trigger/"
	if !strings.HasPrefix(msg.Topic(), prefix) {
		return
	}
	cameraID := strings.TrimPrefix(msg.Topic(), prefix)

	var tm triggerMessage
	if err := json.Unmarshal(msg.Payload(), &tm); err != nil {
		return
	}

	if c.onAction != nil && tm.Action != "" {
		c.onAction(cameraID, tm.Action)
	}
}

// Publish sends a JSON payload to an MQTT topic with QoS 1.
// The topic is prefixed with the client's topic prefix.
func (c *Client) Publish(topic string, payload any) error {
	client := c.getMQTTClient()
	if c == nil || client == nil || !client.IsConnected() {
		return fmt.Errorf("mqtt client not connected")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	fullTopic := c.topicPrefix + "/" + topic
	token := client.Publish(fullTopic, 1, false, data)
	token.Wait()
	if token.Error() != nil {
		return fmt.Errorf("mqtt publish: %w", token.Error())
	}
	return nil
}

// PublishAIDetection publishes an AI detection event to the AI-specific MQTT topic.
// The topic is "ai/{cameraID}" (prefixed by the client's topic prefix).
func (c *Client) PublishAIDetection(ctx context.Context, cameraID string, event string, detections []AiDetectionObj) error {
	client := c.getMQTTClient()
	if c == nil || client == nil || !client.IsConnected() {
		return fmt.Errorf("mqtt client not connected")
	}

	msg := aiTriggerMessage{
		CameraID:   cameraID,
		Event:      event,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Detections: detections,
	}

	topic := "ai/" + cameraID
	return c.Publish(topic, msg)
}
