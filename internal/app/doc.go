// Package app provides the root application orchestrator for MiBee NVR.
//
// # Design
//
// The App owns a set of Service implementations and manages their lifecycle
// with deterministic start/stop ordering. Services register dependencies
// (other managers, hubs, config) at construction time; once Start is called
// no further registration is allowed.
//
// # Extensibility
//
// Public main and third-party extensions both construct the App via RunFree,
// which wires all open-source services. External callers obtain the App,
// type-assert services to the interfaces exposed in sibling pkg/ packages,
// and Register() their own services before Start().
//
// Example (public main):
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
//
// Example (third-party extension):
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	camMgr := a.Value("camera-manager").(camera.Manager)
//	bus := a.Value("eventbus").(eventbus.Bus)
//	extSvc := myextension.New(camMgr, bus)
//	if err := a.Register(extSvc); err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
//
// # Conventions
//
// Service implementations live in internal/* and adapt themselves to this
// interface via an AsService() method (see internal/camera/adapter.go). They
// must be safe for concurrent use after Start returns.
package app
