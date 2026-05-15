// Package main provides the go-plugin wrapper for MockServer.
package main

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

// PluginGRPC is the go-plugin Plugin interface wrapper for MockServer.
// It embeds plugin.NetRPCUnsupportedPlugin to disable net/rpc support
// (gRPC only) and registers the PluginServiceServer with gRPC.
type PluginGRPC struct {
	plugin.NetRPCUnsupportedPlugin
	Impl gen.PluginServiceServer
}

// GRPCServer registers the PluginServiceServer with the gRPC server.
func (p *PluginGRPC) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	gen.RegisterPluginServiceServer(s, p.Impl)
	return nil
}

// GRPCClient creates a PluginServiceClient for the host side.
func (p *PluginGRPC) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return gen.NewPluginServiceClient(c), nil
}
