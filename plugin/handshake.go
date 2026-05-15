// SPDX-License-Identifier: MIT
//
// Shared handshake constants for go-plugin.
// Both the main NVR binary and plugin binaries import this package
// to agree on handshake configuration.

package plugin

import goPlugin "github.com/hashicorp/go-plugin"

const (
	// MagicCookieKey is the environment variable key for the plugin handshake.
	MagicCookieKey = "NVR_PLUGIN"

	// MagicCookieValue is the expected value for the plugin handshake.
	MagicCookieValue = "mibee-nvr-plugin"

	// PluginType is the plugin type name used for dispensing via go-plugin.
	PluginType = "nvr_plugin"
)

// Handshake is the go-plugin handshake config shared between host and plugins.
var Handshake = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   MagicCookieKey,
	MagicCookieValue: MagicCookieValue,
}
