package peer

import (
	"reflect"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// TestApplyICETuning pins the SignalingConfig → SettingEngine wiring for the
// ICE timer/MTU knobs (#13). pion's SettingEngine fields are unexported, so
// the assertions read them via reflection (Int()/Uint() work on unexported
// fields; Interface() would not).
func TestApplyICETuning(t *testing.T) {
	t.Run("zero config = untouched engine (byte-identical defaults)", func(t *testing.T) {
		var se webrtc.SettingEngine
		applyICETuning(&se, SignalingConfig{})
		assertEngineDur(t, &se, "ICEDisconnectedTimeout", 0)
		if got := engineUint(&se, "receiveMTU", t); got != 0 {
			t.Errorf("receiveMTU = %v, want untouched (0)", got)
		}
	})

	t.Run("full override reaches the engine in pion argument order", func(t *testing.T) {
		var se webrtc.SettingEngine
		applyICETuning(&se, SignalingConfig{
			ICETimeouts: ICETimeouts{
				KeepaliveInterval:   4 * time.Second,
				DisconnectedTimeout: 11 * time.Second,
				FailedTimeout:       61 * time.Second,
			},
			ICEReceiveMTU: 1280,
		})
		assertEngineDur(t, &se, "ICEKeepaliveInterval", 4*time.Second)
		assertEngineDur(t, &se, "ICEDisconnectedTimeout", 11*time.Second)
		assertEngineDur(t, &se, "ICEFailedTimeout", 61*time.Second)
		if got := engineUint(&se, "receiveMTU", t); got != 1280 {
			t.Errorf("receiveMTU = %v, want 1280", got)
		}
	})

	t.Run("partial override substitutes library defaults for zero fields", func(t *testing.T) {
		var se webrtc.SettingEngine
		applyICETuning(&se, SignalingConfig{
			ICETimeouts: ICETimeouts{FailedTimeout: 90 * time.Second},
		})
		assertEngineDur(t, &se, "ICEFailedTimeout", 90*time.Second)
		assertEngineDur(t, &se, "ICEKeepaliveInterval", ICEDefaultKeepaliveInterval)
		assertEngineDur(t, &se, "ICEDisconnectedTimeout", ICEDefaultDisconnectedTimeout)
	})

	t.Run("non-positive MTU ignored", func(t *testing.T) {
		var se webrtc.SettingEngine
		applyICETuning(&se, SignalingConfig{ICEReceiveMTU: -1})
		if got := engineUint(&se, "receiveMTU", t); got != 0 {
			t.Errorf("receiveMTU = %v, want untouched", got)
		}
	})
}

func engineDuration(se *webrtc.SettingEngine, t *testing.T, name string) time.Duration {
	t.Helper()
	v := webrtcEngineField(se, name, t)
	if !v.IsValid() {
		return 0
	}
	return time.Duration(v.Int())
}

// assertEngineDur reads a duration leaf field via reflection and compares it.
func assertEngineDur(t *testing.T, se *webrtc.SettingEngine, name string, want time.Duration) {
	t.Helper()
	if got := engineDuration(se, t, name); got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func engineUint(se *webrtc.SettingEngine, name string, t *testing.T) uint64 {
	t.Helper()
	v := webrtcEngineField(se, name, t)
	if !v.IsValid() {
		return 0
	}
	return v.Uint()
}

// webrtcEngineField digs the named leaf field out of the SettingEngine,
// walking nested structs (the ICE timeouts live under the embedded `timeout`
// struct). Unexported fields are read via Int()/Uint() — Interface() would
// panic on them.
func webrtcEngineField(se *webrtc.SettingEngine, name string, t *testing.T) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(*se)
	var found reflect.Value
	var walk func(reflect.Value)
	walk = func(cur reflect.Value) {
		if found.IsValid() {
			return
		}
		for i := 0; i < cur.NumField(); i++ { //nolint:intrange // i indexes both Field(i) and Type().Field(i)
			f := cur.Field(i)
			if cur.Type().Field(i).Name == name {
				found = f
				return
			}
			if f.Kind() == reflect.Struct {
				walk(f)
			}
		}
	}
	walk(v)
	if !found.IsValid() {
		t.Fatalf("SettingEngine has no field %q", name)
	}
	if found.Kind() == reflect.Pointer {
		if found.IsNil() {
			// Never-set optional pointer field == the library default
			// in place; surface as an invalid value that the readers
			// interpret as zero.
			return reflect.Value{}
		}
		found = found.Elem()
	}
	return found
}
