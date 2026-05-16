// SPDX-License-Identifier: MIT
//
// Xiaomi go-plugin bridge for HashiCorp go-plugin gRPC transport.
// Wraps PluginServer for use as a go-plugin Plugin implementation.

package xiaomi

import (
	"context"

	goPlugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

// GRPCPlugin implements go-plugin's Plugin interface for gRPC-only transport.
// The plugin process uses GRPCServer to register the PluginServiceServer;
// the host process uses GRPCClient to obtain a PluginServiceClient.
type GRPCPlugin struct {
	goPlugin.NetRPCUnsupportedPlugin
	Impl gen.PluginServiceServer
}

// GRPCServer registers the Xiaomi PluginServiceServer with the gRPC server.
func (p *GRPCPlugin) GRPCServer(_ *goPlugin.GRPCBroker, s *grpc.Server) error {
	gen.RegisterPluginServiceServer(s, p.Impl)
	return nil
}

// GRPCClient creates a PluginServiceClient from the gRPC connection (host side).
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goPlugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return gen.NewPluginServiceClient(c), nil
}
