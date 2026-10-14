package p2p

// manager_event_hook_test.go — OnEvent 外流钩子（仪表盘连接记录的数据源，
// 2026-10-09 用户需求）：addEvent 落环的同时推给钩子，锁外调用。

import (
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOnEventHookReceivesEvents(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	m := New(config.P2PConfig{}, "127.0.0.1:9090", tokenPath, nil, nil)

	var got []Event
	done := make(chan struct{})
	m.OnEvent = func(ev Event) {
		got = append(got, ev)
		if len(got) == 2 {
			close(done)
		}
	}

	m.addEvent("online", "", "signaling connected")
	m.addEvent("tunnel_up", "sess-1", "clients=1")
	<-done

	// 环内同样可见（旧→新），与钩子收到的一致。
	evs := m.RecentEvents()
	require.Len(t, evs, 2)
	require.Equal(t, "online", evs[0].Type)
	require.Equal(t, "tunnel_up", evs[1].Type)
	require.Equal(t, "sess-1", evs[1].SessionID)
}
