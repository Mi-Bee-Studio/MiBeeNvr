package recorder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

// updateParamSets* rotation gating (#642). The SPS Equal/Different paths are
// covered end-to-end by TestH265Characterization_SPSChangeRotatesSegment (real
// parseable fixtures through a live recorder); here we verify the helper folds
// the gate into `changed` correctly — unclassifiable SPS changes and
// byte-different VPS/PPS rotate once per gate interval, not on every flip.
// (The local testSPS fixture is a 7-byte stub that cannot be parsed, which
// makes it a natural Unknown-class input.)
func TestUpdateParamSets_RotationGating(t *testing.T) {
	now := time.Now()

	t.Run("unclassifiable SPS change rotates once per interval", func(t *testing.T) {
		var gate nalutil.ParamRotationGate
		altSPS := make([]byte, len(testSPS))
		copy(altSPS, testSPS)
		altSPS[len(altSPS)-1] ^= 0x01
		require.Equal(t, nalutil.ParamCompatUnknown, nalutil.CompareSPS(testSPS, altSPS, false),
			"test fixture: the stub SPS must be unclassifiable for this wiring test")

		_, _, changed := updateParamSetsH264([][]byte{altSPS, testPPS}, testSPS, testPPS, &gate, now)
		require.True(t, changed, "first unclassifiable change rotates (lastRotate is zero)")

		_, _, changed = updateParamSetsH264([][]byte{altSPS, testPPS}, testSPS, testPPS, &gate, now.Add(5*time.Second))
		require.False(t, changed, "rapid unclassifiable flip-flop is rate-limited")
	})

	t.Run("PPS alternation rate-limited", func(t *testing.T) {
		var gate nalutil.ParamRotationGate
		altPPS := make([]byte, len(testPPS))
		copy(altPPS, testPPS)
		altPPS[len(altPPS)-1] ^= 0x01

		_, _, changed := updateParamSetsH264([][]byte{testSPS, altPPS}, testSPS, testPPS, &gate, now)
		require.True(t, changed, "first PPS change rotates")

		_, _, changed = updateParamSetsH264([][]byte{testSPS, altPPS}, testSPS, altPPS, &gate, now.Add(5*time.Second))
		require.False(t, changed, "rapid PPS flip-flop is rate-limited")
	})

	t.Run("H265 VPS alternation rate-limited", func(t *testing.T) {
		var gate nalutil.ParamRotationGate
		altVPS := make([]byte, len(testVPS265))
		copy(altVPS, testVPS265)
		altVPS[len(altVPS)-1] ^= 0x01

		_, _, _, changed := updateParamSetsH265([][]byte{altVPS, testSPS265, testPPS265}, testVPS265, testSPS265, testPPS265, &gate, now)
		require.True(t, changed, "first VPS change rotates")

		_, _, _, changed = updateParamSetsH265([][]byte{altVPS, testSPS265, testPPS265}, altVPS, testSPS265, testPPS265, &gate, now.Add(5*time.Second))
		require.False(t, changed, "rapid VPS flip-flop is rate-limited")
	})

	t.Run("unchanged params never rotate", func(t *testing.T) {
		var gate nalutil.ParamRotationGate
		_, _, changed := updateParamSetsH264([][]byte{testSPS, testPPS}, testSPS, testPPS, &gate, now)
		require.False(t, changed, "byte-identical param sets must not report a change")
	})
}
