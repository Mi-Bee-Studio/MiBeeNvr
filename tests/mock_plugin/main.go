// Command mock-plugin is a mock NVR plugin for integration testing.
//
// It implements the full gRPC PluginService and sends synthetic H.264
// NAL frames when StartRecorder is called. The NVR host process launches
// this binary via HashiCorp go-plugin.
package main

import (
	sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"
	"github.com/hashicorp/go-plugin"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: sharedPlugin.Handshake,
		Plugins: map[string]plugin.Plugin{
			"plugin": &PluginGRPC{Impl: NewMockServer()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
