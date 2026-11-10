package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

var logger = slogx.Component("onboarding")

// Job states for the async scan/adopt operations.
const (
	JobRunning = "running"
	JobDone    = "done"
	JobError   = "error"
)

// Job is one async operation (scan or adopt). The API surfaces snapshots.
type Job struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"` // "scan" | "adopt"
	State     string       `json:"state"`
	StartedAt time.Time    `json:"started_at"`
	Error     string       `json:"error,omitempty"`
	Candidate []Candidate  `json:"candidates,omitempty"`
	Adopt     *AdoptResult `json:"adopt,omitempty"`
}

// Status is the /api/onboarding/status payload.
type Status struct {
	Enabled       bool   `json:"enabled"`
	Subnet        string `json:"subnet,omitempty"`
	AliasIP       string `json:"alias_ip,omitempty"`
	AliasActive   bool   `json:"alias_active"`
	LastScan      string `json:"last_scan,omitempty"`
	LastScanFound int    `json:"last_scan_found"`
}

// Manager runs zero-touch camera onboarding: subnet scans (bounded sweeps)
// and guided adopt operations (ONVIP network change + enroll), exposed to
// the API as async jobs.
// CameraEnroller is the enrollment seam (the camera manager satisfies it
// in production; tests fake it).
type CameraEnroller interface {
	AddCamera(ctx context.Context, cam config.CameraConfig) (string, error)
}

type Manager struct {
	cfg      func() *config.Config
	enroller CameraEnroller
	alias    AliasManager

	wg        sync.WaitGroup
	mu        sync.Mutex
	jobs      map[string]*Job
	lastScan  time.Time
	lastFound int
	opMu      sync.Mutex // one scan-or-adopt at a time (alias exclusivity)

	// seams (tests swap these; NewManager sets production defaults)
	setNet       func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error)
	serialAt     func(ctx context.Context, ip string) string
	reach        func(ctx context.Context, ip, user, pass string) bool
	authedSerial func(ctx context.Context, ip, user, pass string) string
	sweepTargets func() []string
}

// NewManager builds the onboarding manager with production seams.
func NewManager(cfg func() *config.Config, camMgr *camera.CameraManager) *Manager {
	m := &Manager{
		cfg:   cfg,
		alias: NewAliasManager(),
		jobs:  make(map[string]*Job),
	}
	if camMgr != nil {
		m.enroller = camMgr
	}
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		client := onvif.NewClient(onvifEndpoint(ip), user, pass)
		ifaces, err := client.GetNetworkInterfaces(ctx)
		if err != nil {
			return false, fmt.Errorf("GetNetworkInterfaces: %w", err)
		}
		if len(ifaces) == 0 || ifaces[0].Token == "" {
			return false, errors.New("device reports no network interfaces")
		}
		return client.SetNetworkInterfaces(ctx, ifaces[0].Token, dhcp, staticIP, prefix)
	}
	m.serialAt = probeSerial
	m.sweepTargets = primarySubnetTargets
	m.reach = func(ctx context.Context, ip, user, pass string) bool {
		client := onvif.NewClient(onvifEndpoint(ip), user, pass)
		_, err := client.GetDeviceInformation(ctx)
		return err == nil
	}
	m.authedSerial = func(ctx context.Context, ip, user, pass string) string {
		client := onvif.NewClient(onvifEndpoint(ip), user, pass)
		info, err := client.GetDeviceInformation(ctx)
		if err != nil {
			return ""
		}
		return info.SerialNumber
	}
	return m
}

func onvifEndpoint(ip string) string {
	return "http://" + ip + "/onvif/device_service"
}

// newJobID returns a short random job id.
func newJobID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Status snapshots the service state for the API.
func (m *Manager) Status() Status {
	cfg := m.cfg()
	o := &cfg.CameraOnboarding
	st := Status{Enabled: o.Enabled}
	if o.Enabled {
		st.Subnet = strings.TrimSpace(o.DefaultSubnet)
		if a := o.ResolveOnboardingAliasIP(); a != nil {
			st.AliasIP = a.String()
		}
		m.mu.Lock()
		if !m.lastScan.IsZero() {
			st.LastScan = m.lastScan.UTC().Format(time.RFC3339)
			st.LastScanFound = m.lastFound
		}
		st.AliasActive = m.alias.UpHeld(o.ResolveOnboardingAliasIP())
		m.mu.Unlock()
	}
	return st
}

// StartScan runs a bounded sweep of the factory-default subnet and records
// the candidates as a job. Synchronous — the caller wraps in the job
// machinery.
func (m *Manager) StartScan(ctx context.Context) ([]Candidate, error) {
	cfg := m.cfg()
	o := &cfg.CameraOnboarding
	if !o.Enabled {
		return nil, errors.New("camera onboarding is disabled")
	}

	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(o.DefaultSubnet))
	if err != nil {
		return nil, fmt.Errorf("camera_onboarding.default_subnet: %w", err)
	}
	aliasIP := o.ResolveOnboardingAliasIP()

	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.alias.Up(aliasIP, 24); err != nil {
		return nil, fmt.Errorf("alias bind: %w", err)
	}
	defer func() {
		if derr := m.alias.Down(aliasIP); derr != nil {
			logger.Warn("scan: alias removal failed", "ip", aliasIP, "error", derr)
		}
	}()

	targets := expandSubnet(ipnet)
	cands := ScanSubnet(ctx, targets, scanHostTimeout, scanParallel)

	m.mu.Lock()
	m.lastScan = time.Now()
	m.lastFound = len(cands)
	m.mu.Unlock()
	return cands, nil
}

// expandSubnet turns a CIDR into ip:80 probe targets (bounded /24;
// wider networks collapse to their /24 base to keep RPi-class sweeps sane).
func expandSubnet(ipnet *net.IPNet) []string {
	base := ipnet.IP.To4()
	mask := ipnet.Mask
	if len(mask) != 4 {
		return nil
	}
	if mask[0] != 255 || mask[1] != 255 || mask[2] != 255 {
		// Wider than /24: clamp to the /24 containing the network address.
		mask = net.CIDRMask(24, 32)
		base = base.Mask(mask)
	}
	targets := make([]string, 0, 254)
	for i := 1; i < 255; i++ {
		targets = append(targets, fmt.Sprintf("%d.%d.%d.%d:80", base[0], base[1], base[2], i))
	}
	return targets
}

// ---- async job surface (API-facing) ----

// StartScanJob launches a scan in the background and returns the job id.
func (m *Manager) StartScanJob() (string, error) {
	cfg := m.cfg()
	if !cfg.CameraOnboarding.Enabled {
		return "", errors.New("camera onboarding is disabled")
	}
	id := newJobID()
	job := &Job{ID: id, Kind: "scan", State: JobRunning, StartedAt: time.Now()}
	m.mu.Lock()
	m.pruneJobsLocked()
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		cands, err := m.StartScan(context.Background())
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			job.State = JobError
			job.Error = err.Error()
			return
		}
		job.State = JobDone
		job.Candidate = cands
	}()
	return id, nil
}

// StartAdoptJob launches an adopt operation and returns the job id.
func (m *Manager) StartAdoptJob(req AdoptRequest) (string, error) {
	cfg := m.cfg()
	if !cfg.CameraOnboarding.Enabled {
		return "", errors.New("camera onboarding is disabled")
	}
	id := newJobID()
	job := &Job{ID: id, Kind: "adopt", State: JobRunning, StartedAt: time.Now()}
	m.mu.Lock()
	m.pruneJobsLocked()
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		res, err := m.Adopt(context.Background(), req)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			job.State = JobError
			job.Error = err.Error()
			return
		}
		job.State = JobDone
		job.Adopt = res
	}()
	return id, nil
}

// Job returns a job snapshot.
func (m *Manager) Job(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	cp := *job
	return cp, true
}

// pruneJobs caps the registry (a patrol-size deployment never accrues
// hundreds of jobs; this is hygiene for long uptimes).
func (m *Manager) pruneJobsLocked() {
	if len(m.jobs) <= 64 {
		return
	}
	oldest := ""
	var oldestT time.Time
	for id, j := range m.jobs {
		if j.State == JobRunning {
			continue
		}
		if oldest == "" || j.StartedAt.Before(oldestT) {
			oldest, oldestT = id, j.StartedAt
		}
	}
	if oldest != "" {
		delete(m.jobs, oldest)
	}
}

// Start runs the periodic background sweep (scan_interval_s > 0 only).
// Always non-fatal: discovery is a convenience path. Implements the
// service contract pkg/app expects (Start/Stop pair).
func (m *Manager) Start(ctx context.Context) error {
	cfg := m.cfg()
	interval := cfg.CameraOnboarding.ScanIntervalS
	if !cfg.CameraOnboarding.Enabled || interval <= 0 {
		return nil // manual-scan-only mode: nothing to run
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jobID, err := m.StartScanJob()
				if err != nil {
					logger.Warn("onboarding: periodic scan failed to start", "error", err)
					continue
				}
				logger.Info("onboarding: periodic scan started", "job", jobID)
			}
		}
	}()
	return nil
}

// Stop waits for in-flight jobs to settle.
func (m *Manager) Stop() error {
	m.wg.Wait()
	return nil
}
