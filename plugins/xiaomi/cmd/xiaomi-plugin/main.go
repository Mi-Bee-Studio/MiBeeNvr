// SPDX-License-Identifier: MIT
//
// Command xiaomi-plugin is the standalone Xiaomi camera gRPC plugin binary.
// The NVR host process launches this via HashiCorp go-plugin.
// It connects to Xiaomi cameras via MISS/CS2 protocol and streams
// NAL frames back to the host over gRPC.

package main

import (
	"context"

	sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"
	"github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	xiaomi "github.com/Mi-Bee-Studio/MiBeeNvr/plugins/xiaomi"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// XiaomiPluginGRPC wraps PluginServer for the go-plugin framework.
type XiaomiPluginGRPC struct {
	plugin.NetRPCUnsupportedPlugin
	Impl gen.PluginServiceServer
}

// GRPCServer registers the PluginServiceServer with the gRPC server.
func (p *XiaomiPluginGRPC) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	gen.RegisterPluginServiceServer(s, p.Impl)
	return nil
}

// GRPCClient creates a PluginServiceClient for the host side.
func (p *XiaomiPluginGRPC) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return gen.NewPluginServiceClient(c), nil
}

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: sharedPlugin.Handshake,
		Plugins: map[string]plugin.Plugin{
			"plugin": &XiaomiPluginGRPC{Impl: xiaomi.NewPluginServer()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
