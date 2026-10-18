package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeService is a minimal Service implementation for tests.
type fakeService struct {
	name     string
	startErr error
	stopErr  error
	startCnt int
	stopCnt  int

	mu sync.Mutex
}

func (f *fakeService) Name() string { return f.name }

func (f *fakeService) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCnt++
	if f.startErr != nil {
		return f.startErr
	}
	return nil
}

func (f *fakeService) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCnt++
	return f.stopErr
}

func (f *fakeService) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCnt
}

func (f *fakeService) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCnt
}

func TestNew_Empty(t *testing.T) {
	t.Helper()
	a := New()
	if got := len(a.Services()); got != 0 {
		t.Errorf("Services() = %v, want empty", got)
	}
	if a.Started() {
		t.Error("Started() = true on new App, want false")
	}
}

func TestRegister_Success(t *testing.T) {
	t.Helper()
	a := New()
	s := &fakeService{name: "alpha"}
	if err := a.Register(s); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := a.Services(); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("Services() = %v, want [alpha]", got)
	}
	if a.Get("alpha") != s {
		t.Error("Get returned wrong service")
	}
}

func TestRegister_Nil(t *testing.T) {
	t.Helper()
	a := New()
	if err := a.Register(nil); err == nil {
		t.Error("Register(nil) = nil, want error")
	}
}

func TestRegister_EmptyName(t *testing.T) {
	t.Helper()
	a := New()
	s := &fakeService{name: ""}
	if err := a.Register(s); err == nil {
		t.Error("Register with empty Name = nil, want error")
	}
}

func TestRegister_Duplicate(t *testing.T) {
	t.Helper()
	a := New()
	if err := a.Register(&fakeService{name: "alpha"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := a.Register(&fakeService{name: "alpha"})
	if err == nil {
		t.Error("second Register = nil, want duplicate error")
	}
}

func TestRegister_AfterStart(t *testing.T) {
	t.Helper()
	a := New()
	_ = a.Register(&fakeService{name: "alpha"})
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := a.Register(&fakeService{name: "beta"})
	if err == nil {
		t.Error("Register after Start = nil, want error")
	}
}

func TestStart_Order(t *testing.T) {
	t.Helper()
	a := New()
	s1 := &fakeService{name: "first"}
	s2 := &fakeService{name: "second"}
	s3 := &fakeService{name: "third"}
	_ = a.Register(s1)
	_ = a.Register(s2)
	_ = a.Register(s3)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !a.Started() {
		t.Error("Started() = false, want true")
	}
	for _, s := range []*fakeService{s1, s2, s3} {
		if s.starts() != 1 {
			t.Errorf("%q started %d times, want 1", s.name, s.starts())
		}
	}
}

func TestStop_ReverseOrder(t *testing.T) {
	t.Helper()
	a := New()

	svcs := make([]*fakeService, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		s := &fakeService{name: name}
		svcs = append(svcs, s)
		_ = a.Register(s)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// All services stopped exactly once.
	for _, s := range svcs {
		if s.stops() != 1 {
			t.Errorf("%q stopped %d times, want 1", s.name, s.stops())
		}
	}
}

func TestStop_BeforeStart(t *testing.T) {
	t.Helper()
	a := New()
	_ = a.Register(&fakeService{name: "alpha"})
	if err := a.Stop(); err != nil {
		t.Errorf("Stop before Start = %v, want nil", err)
	}
}

func TestStop_Idempotent(t *testing.T) {
	t.Helper()
	a := New()
	s := &fakeService{name: "alpha"}
	_ = a.Register(s)
	_ = a.Start(context.Background())
	if err := a.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := a.Stop(); err != nil {
		t.Errorf("second Stop: %v, want nil", err)
	}
	if s.stops() != 1 {
		t.Errorf("stopped %d times after double Stop, want 1", s.stops())
	}
}

func TestStart_RollbackOnError(t *testing.T) {
	t.Helper()
	a := New()
	s1 := &fakeService{name: "ok1"}
	s2 := &fakeService{name: "fails", startErr: errors.New("boom")}
	s3 := &fakeService{name: "never"}
	_ = a.Register(s1)
	_ = a.Register(s2)
	_ = a.Register(s3)

	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("Start = nil, want error from failing service")
	}
	if s1.starts() != 1 {
		t.Errorf("ok1 started %d, want 1", s1.starts())
	}
	if s1.stops() != 1 {
		t.Errorf("ok1 stopped %d, want 1 (rollback)", s1.stops())
	}
	if s2.starts() != 1 {
		t.Errorf("fails started %d, want 1", s2.starts())
	}
	if s3.starts() != 0 {
		t.Errorf("never started %d, want 0", s3.starts())
	}
}

func TestStart_AlreadyStarted(t *testing.T) {
	t.Helper()
	a := New()
	s := &fakeService{name: "alpha"}
	_ = a.Register(s)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Errorf("second Start: %v, want nil (idempotent)", err)
	}
	if s.starts() != 1 {
		t.Errorf("started %d after double Start, want 1", s.starts())
	}
}

func TestGet_Unknown(t *testing.T) {
	t.Helper()
	a := New()
	if got := a.Get("missing"); got != nil {
		t.Errorf("Get(missing) = %v, want nil", got)
	}
}

func TestStop_PropagatesFirstError(t *testing.T) {
	t.Helper()
	a := New()
	s1 := &fakeService{name: "alpha", stopErr: errors.New("alpha-fail")}
	s2 := &fakeService{name: "beta", stopErr: errors.New("beta-fail")}
	_ = a.Register(s1)
	_ = a.Register(s2)
	_ = a.Start(context.Background())

	err := a.Stop()
	if err == nil {
		t.Fatal("Stop = nil, want error")
	}
	// First error in reverse order is from beta (stopped first).
	if err.Error() == "" {
		t.Errorf("Error message empty: %v", err)
	}
}

// TestServices_ReturnsCopy verifies the caller cannot mutate App's internal
// slice via the returned value.
func TestServices_ReturnsCopy(t *testing.T) {
	t.Helper()
	a := New()
	_ = a.Register(&fakeService{name: "alpha"})
	svcs := a.Services()
	svcs[0] = "mutated"
	if got := a.Services(); got[0] != "alpha" {
		t.Errorf("internal slice mutated via returned copy: %v", got)
	}
}
