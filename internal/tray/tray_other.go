//go:build !windows

package tray

// startPlatform is a silent no-op on non-windows platforms — the tray is a
// desktop-management affordance that only makes sense there.
func startPlatform(opts Options) (stop func(), quit <-chan struct{}, err error) {
	never := make(chan struct{})
	return func() {}, never, nil
}

// SetAddress is the no-op counterpart of the windows tray retarget — on
// macOS the menu-bar helper is refreshed by internal/install instead.
func SetAddress(listen string) {}
