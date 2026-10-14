package peer

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// TestApplyAdvertiseAddr 钉死公网端点通告的校验语义：空值 no-op（存量
// 行为零变化）；坏地址/0 端口/超范围端口拒绝；合法值能让完整 PC 工厂
// 走通（pion 设置兼容性烟测——NAT1To1 host 模式下创建 PC 不报错）。
func TestApplyAdvertiseAddr(t *testing.T) {
	var se webrtc.SettingEngine
	require.NoError(t, applyAdvertiseAddr(&se, ""), "empty must be a no-op")
	require.Error(t, applyAdvertiseAddr(&se, "no-port"))
	require.Error(t, applyAdvertiseAddr(&se, "1.2.3.4"))
	require.Error(t, applyAdvertiseAddr(&se, "1.2.3.4:0"))
	require.Error(t, applyAdvertiseAddr(&se, "1.2.3.4:99999"))
	require.Error(t, applyAdvertiseAddr(&se, "1.2.3.4:notaport"))
	require.NoError(t, applyAdvertiseAddr(&se, "203.0.113.5:41913"))
	require.NoError(t, applyAdvertiseAddr(&se, "[2001:db8::1]:41913"),
		"IPv6 literal must parse")

	// Full factory smoke: settings survive into a real PeerConnection.
	// (Socket binding happens at gathering, not here — no port conflicts.)
	pc, err := newPeerConnection(nil, "203.0.113.5:41913")
	require.NoError(t, err)
	require.NoError(t, pc.Close())

	_, err = newPeerConnection(nil, "definitely-bad")
	require.Error(t, err, "invalid advertise must fail PC creation loudly")
}
