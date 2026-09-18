//go:build !windows

package tray

// startPlatform is a silent no-op on non-windows platforms — the tray is a
// desktop-management affordance that only makes sense there.
func startPlatform(opts Options) (stop func(), quit <-chan struct{}, err error) {
	never := make(chan struct{})
	return func() {}, never, nil
}
