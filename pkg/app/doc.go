// Package app provides the root application orchestrator for MiBee NVR.
//
// # Design
//
// The App owns a set of Service implementations and manages their lifecycle
// with deterministic start/stop ordering. Services register dependencies
// (other managers, hubs, config) at construction time; once Start is called
// no further registration is allowed.
//
// # Open source vs commercial integration
//
// Public main and commercial MiBeeNvrP2P both construct the App via RunFree,
// which wires all open-source services. Commercial callers obtain the App,
// type-assert services to the interfaces exposed in sibling pkg/ packages,
// and Register() their own services before Start().
//
// Example (public main):
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
//
// Example (commercial main):
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	camSvc := a.Get("camera").(camera.Manager)
//	p2pSvc := p2p.New(camSvc, a.Get("eventbus").(eventbus.Bus))
//	if err := a.Register(p2pSvc); err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
//
// # Conventions
//
// Service implementations live in internal/* and adapt themselves to this
// interface via an AsService() method (see internal/camera/adapter.go). They
// must be safe for concurrent use after Start returns.
package app
