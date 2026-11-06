package app

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
)

// licenseStateGaugeValue maps the license state machine onto the
// nvr_license_state gauge (0=none … 4=valid). Ordered so that HIGHER is
// always BETTER — alert expressions can threshold on it (< 3 = needs
// attention when licensing is enforced).
func licenseStateGaugeValue(m *license.Manager) func() float64 {
	return func() float64 {
		switch m.Status().State {
		case license.StateValid:
			return 4
		case license.StateExpiringSoon:
			return 3
		case license.StateExpired:
			return 2
		case license.StateInvalid:
			return 1
		default: // StateNone or anything unforeseen
			return 0
		}
	}
}
