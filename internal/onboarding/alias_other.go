//go:build !linux

package onboarding

import (
	"errors"
	"net"
)

// stubAliasManager reports the platform gap clearly: zero-touch onboarding
// manipulates interface addresses with rtnetlink, which is Linux-only. The
// dev hosts (Windows/macOS) never run the enabled path.
type stubAliasManager struct{}

func newPlatformAliasManager() AliasManager { return stubAliasManager{} }

func (stubAliasManager) Up(net.IP, int) error {
	return errors.New("camera onboarding: IP aliasing requires Linux (rtnetlink); this platform is not supported")
}

func (stubAliasManager) Down(_ net.IP) error { return nil }

func (stubAliasManager) UpHeld(_ net.IP) bool { return false }

var _ AliasManager = stubAliasManager{}
