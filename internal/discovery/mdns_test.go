package discovery

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMDNSRegistrarConstructor(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("dev-id-1", "客厅录像机", 9090, true)

	require.Equal(t, "mdns", r.Name())
	require.Equal(t, "客厅录像机", r.instance, "instance is the plain device name")
	require.Equal(t, 9090, r.port)

	// TXT must match the discovery contract field-for-field.
	want := map[string]string{
		"ver":  "1",
		"id":   "dev-id-1",
		"name": url.QueryEscape("客厅录像机"),
		"tls":  "1",
		"api":  "9090",
	}
	require.Len(t, r.txt, len(want))
	for _, kv := range r.txt {
		var k, v string
		for i := range len(kv) {
			if kv[i] == '=' {
				k, v = kv[:i], kv[i+1:]
				break
			}
		}
		require.Contains(t, want, k, "unexpected TXT key %q", k)
		require.Equal(t, want[k], v, "TXT %s mismatch", k)
	}
}

func TestMDNSRegistrarPlainTextName(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("id-2", "bananapim5", 9091, false)
	for _, kv := range r.txt {
		if len(kv) > 5 && kv[:5] == "name=" {
			require.Equal(t, "name=bananapim5", kv)
		}
		if len(kv) > 4 && kv[:4] == "tls=" {
			require.Equal(t, "tls=0", kv)
		}
	}
}

func TestMDNSRegistrarStopWithoutStart(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("id-3", "host", 9090, false)
	require.NoError(t, r.Stop(), "Stop before Start must be a no-op")
	require.NoError(t, r.Stop(), "Stop must be idempotent")
}
