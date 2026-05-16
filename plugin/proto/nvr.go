// Package proto provides the Protocol Buffers SDK for NVR plugin communication.
//
// This package re-exports the generated types from the gen/ sub-package for
// clean imports throughout the codebase. Import this package (not gen/) to
// access the NVR plugin protocol types.
//
// Generated code is in the gen/ sub-package to keep the go:generate directive
// and generated files isolated. This file re-exports the key types so that
// consumers can import:
//
//	import proto "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
//
// The gen.go file in this package contains the go:generate directive that
// produces the generated Go code from nvr.proto.
package proto
