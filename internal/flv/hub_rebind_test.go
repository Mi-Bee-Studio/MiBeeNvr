// SPDX-License-Identifier: MIT
//
// Hub identity coverage: ActiveHub/RebindHub — the machinery that keeps an
// FLV entry listening to the RIGHT StreamHub across sub-stream puller
// recycles (#513).

package flv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func TestActiveHubAndRebind(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() {
		m.UnregisterStream("cam-flv")
		m.UnregisterStream("cam-old")
	})

	require.Nil(t, m.ActiveHub("cam-none"))

	hub1 := model.NewStreamHub()
	require.NoError(t, m.RegisterStream("cam-flv", model.FormatH264, minimalSPS, minimalPPS, nil, hub1))
	require.Equal(t, hub1, m.ActiveHub("cam-flv"))

	// Rebind to a fresh hub: old hub's consumer is removed, frames now flow
	// from the new hub (a probe consumer on hub2 sees broadcasts).
	hub2 := model.NewStreamHub()
	m.RebindHub("cam-flv", hub2)
	require.Equal(t, hub2, m.ActiveHub("cam-flv"))

	probeCh := make(chan struct{}, 1)
	require.NoError(t, hub2.SubscribeMsg("probe", func(model.FrameMsg) {
		select {
		case probeCh <- struct{}{}:
		default:
		}
	}))
	hub2.Broadcast(1000, [][]byte{minimalSPS, {0x65, 0x88}}, true)
	require.Eventually(t, func() bool {
		select {
		case <-probeCh:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "rebound hub never delivered frames")

	// Guards: nil hub and unknown camera are no-ops.
	m.RebindHub("cam-flv", nil)
	require.Equal(t, hub2, m.ActiveHub("cam-flv"))
	m.RebindHub("cam-none", hub2)
	require.Nil(t, m.ActiveHub("cam-none"))
}

func TestRebindHubUnsubscribesOldHub(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { m.UnregisterStream("cam-flv"); m.UnregisterStream("cam-old") })

	hub1 := model.NewStreamHub()
	require.NoError(t, m.RegisterStream("cam-old", model.FormatH264, minimalSPS, minimalPPS, nil, hub1))

	// While registered, the entry owns the "flv-cam-old" consumer slot on hub1.
	require.Error(t, hub1.SubscribeMsg("flv-cam-old", func(model.FrameMsg) {}))

	hub2 := model.NewStreamHub()
	m.RebindHub("cam-old", hub2)

	// Rebinding released the old hub's consumer slot: the same ID can be
	// taken over by someone else.
	require.NoError(t, hub1.SubscribeMsg("flv-cam-old", func(model.FrameMsg) {}))
	hub1.Unsubscribe("flv-cam-old")
}
