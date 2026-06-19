package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Service is a lifecycle-managed component of the App.
//
// Implementations should be safe for concurrent use after Start returns.
// Stop must be idempotent (safe to call multiple times) and should release
// all goroutines, file handles, and network resources held by the service.
type Service interface {
	// Name uniquely identifies the service in the App's registry.
	// Two services with the same Name cannot coexist.
	Name() string

	// Start begins the service's operation. It must return promptly;
	// long-running work should run on goroutines that honor ctx cancellation.
	// Returning a non-nil error aborts the App start and triggers rollback.
	Start(ctx context.Context) error

	// Stop terminates the service gracefully. It must release all resources.
	// It is called in reverse registration order during App.Stop.
	Stop() error
}

// App is the root application orchestrator.
//
// It manages a set of Services with deterministic start (registration order)
// and stop (reverse registration order) sequencing. App is safe for
// concurrent use after construction.
type App struct {
	mu       sync.Mutex
	services map[string]Service
	order    []string // registration order, for deterministic start/stop
	values   map[string]any // typed values for retrieval via Value()
	started  bool
	stopped  bool
}

// New creates an empty App ready for service registration.
func New() *App {
	return &App{
		services: make(map[string]Service),
		values:   make(map[string]any),
	}
}

// Register adds a Service to the App.
//
// Returns an error if:
//   - the service is nil,
//   - another service with the same Name is already registered,
//   - Start has already been called (registration after Start is forbidden).
//
// Register must be called before Start. The Pro extension path:
// call RunFree → Register Pro services → Start.
func (a *App) Register(s Service) error {
	if s == nil {
		return errors.New("app: cannot register nil service")
	}
	name := s.Name()
	if name == "" {
		return errors.New("app: service Name() cannot be empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("app: cannot register service %q after Start", name)
	}
	if _, exists := a.services[name]; exists {
		return fmt.Errorf("app: service %q already registered", name)
	}
	a.services[name] = s
	a.values[name] = s // also expose for typed retrieval
	a.order = append(a.order, name)
	slog.Debug("app: registered service", "name", name)
	return nil
}

// Get returns the Service with the given name, or nil if not registered.
//
// The returned Service can be type-asserted to a specific interface
// exposed by a sibling pkg/ package (e.g., pkg/camera.Manager):
//	camSvc := a.Get("camera")
//	if camSvc == nil { return errors.New("camera service not registered") }
//	camMgr := camSvc.(camera.Manager)
//
// For values that are not lifecycle Services (e.g., wrapper adapters),
// use Value() instead.
func (a *App) Get(name string) Service {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.services[name]
}

// RegisterValue stores an arbitrary value under name, retrievable via Value.
// Unlike Register, this does NOT add the value to the service lifecycle.
// Use it to expose typed handles (e.g., a pkg/camera.Manager wrapper) for
// out-of-module consumers when the underlying concrete type cannot satisfy
// both pkg/app.Service and the public interface simultaneously
// (e.g., due to method-name conflicts).
//
// Returns an error if name is already used by either a Service or a value,
// or if Start has already been called.
func (a *App) RegisterValue(name string, v any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("app: cannot register value %q after Start", name)
	}
	if _, exists := a.services[name]; exists {
		return fmt.Errorf("app: name %q already registered as service", name)
	}
	if _, exists := a.values[name]; exists {
		return fmt.Errorf("app: value %q already registered", name)
	}
	a.values[name] = v
	slog.Debug("app: registered value", "name", name)
	return nil
}

// Value returns the value registered under name, or nil if not registered.
// The caller is expected to type-assert:
//
	//	m := a.Value("camera-manager").(camera.Manager)
func (a *App) Value(name string) any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.values[name]
}

// Services returns the names of all registered services in registration order.
// The returned slice is a copy; callers may mutate it freely.
func (a *App) Services() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.order))
	copy(out, a.order)
	return out
}

// Start starts all registered services in registration order.
//
// If any service fails to start, already-started services are stopped in
// reverse order before returning the error (rollback). Start is idempotent
// in the failure sense: a second call after success returns nil; a second
// call after failure returns the original error.
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	order := make([]string, len(a.order))
	copy(order, a.order)
	a.mu.Unlock()

	var started []string
	for _, name := range order {
		s := a.services[name]
		slog.Info("app: starting service", "name", name)
		if err := s.Start(ctx); err != nil {
			slog.Error("app: service failed to start",
				"name", name, "error", err)
			// Rollback in reverse order.
			for i := len(started) - 1; i >= 0; i-- {
				rbName := started[i]
				if rbErr := a.services[rbName].Stop(); rbErr != nil {
					slog.Error("app: service failed to stop during rollback",
						"name", rbName, "error", rbErr)
				}
			}
			return fmt.Errorf("app: start service %q: %w", name, err)
		}
		started = append(started, name)
	}
	slog.Info("app: all services started",
		"count", len(started))
	return nil
}

// Stop stops all registered services in reverse registration order.
//
// Stop is idempotent: repeated calls return nil. Errors from individual
// services are logged; the first non-nil error is returned to the caller
// after all services have been stopped.
func (a *App) Stop() error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil
	}
	a.stopped = true
	order := make([]string, len(a.order))
	copy(order, a.order)
	a.mu.Unlock()

	var firstErr error
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]
		s := a.services[name]
		slog.Info("app: stopping service", "name", name)
		if err := s.Stop(); err != nil {
			slog.Error("app: service failed to stop",
				"name", name, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("app: stop service %q: %w", name, err)
			}
		}
	}
	if firstErr == nil {
		slog.Info("app: all services stopped", "count", len(order))
	}
	return firstErr
}

// Started reports whether Start has been called successfully.
func (a *App) Started() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started && !a.stopped
}
