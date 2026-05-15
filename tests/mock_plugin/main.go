// Command mock-plugin is a mock NVR plugin for integration testing.
//
// It implements the full gRPC PluginService and sends synthetic H.264
// NAL frames when StartRecorder is called. The NVR host process launches
// this binary via HashiCorp go-plugin.
package main

import (
	"github.com/hashicorp/go-plugin"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: plugin.HandshakeConfig{
			ProtocolVersion:  1,
			MagicCookieKey:   "NVR_PLUGIN",
			MagicCookieValue: "nvr-plugin",
		},
		Plugins: map[string]plugin.Plugin{
			"plugin": &PluginGRPC{Impl: NewMockServer()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
