package mqtt

import (
	"context"
	"encoding/json"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// triggerMessage is the JSON payload of a camera trigger event.
type triggerMessage struct {
	Action string `json:"action"`
}

// Client subscribes to MQTT topics for camera trigger events.
type Client struct {
	brokerURL   string
	clientID    string
	topicPrefix string
	mqttClient  mqtt.Client
	onAction    func(cameraID string, action string)
}

// NewClient creates a new MQTT trigger event subscriber.
func NewClient(brokerURL, clientID, topicPrefix string, onAction func(cameraID, action string)) *Client {
	return &Client{
		brokerURL:   brokerURL,
		clientID:    clientID,
		topicPrefix: topicPrefix,
		onAction:    onAction,
	}
}

// IsConfigured returns true if the broker URL is non-empty.
func (c *Client) IsConfigured() bool {
	return c.brokerURL != ""
}

// Start connects to the MQTT broker and subscribes to trigger events.
// It blocks until ctx is cancelled. If MQTT is not configured, it returns immediately.
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

	c.mqttClient = mqtt.NewClient(opts)
	token := c.mqttClient.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

// Stop disconnects gracefully from the MQTT broker.
func (c *Client) Stop() error {
	if c.mqttClient != nil && c.mqttClient.IsConnected() {
		c.mqttClient.Disconnect(1000)
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
