package gb28181

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTalkUplinkRegistryFanout(t *testing.T) {
	r := NewTalkUplinkRegistry()

	ch1, unsub1 := r.Subscribe("chan-a")
	ch2, _ := r.Subscribe("chan-a")
	other, _ := r.Subscribe("chan-b")

	r.Dispatch("chan-a", []byte{0xD5})
	for _, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case p := <-ch:
			require.Equal(t, []byte{0xD5}, p)
		case <-time.After(2 * time.Second):
			t.Fatal("subscriber did not receive the dispatch")
		}
	}
	select {
	case <-other:
		t.Fatal("dispatch leaked to another channel")
	default:
	}

	unsub1()
	_, open := <-ch1
	require.False(t, open, "unsubscribe must close the channel")

	r.Dispatch("chan-a", []byte{0x55})
	select {
	case p := <-ch2:
		require.Equal(t, []byte{0x55}, p)
	case <-time.After(2 * time.Second):
		t.Fatal("remaining subscriber must still receive")
	}
	select {
	case _, open := <-ch1:
		require.False(t, open)
	default:
		t.Fatal("closed channel must read closed, not hang")
	}
}

func TestTalkUplinkRegistryDropWhenFull(t *testing.T) {
	r := NewTalkUplinkRegistry()
	ch, unsub := r.Subscribe("chan-a")
	defer unsub()

	// Fill past the buffer: dispatch must never block the producer.
	for i := range 500 {
		r.Dispatch("chan-a", []byte{byte(i)})
	}
	require.Equal(t, cap(ch), len(ch), "buffer full, overflow dropped")

	got := <-ch
	require.Equal(t, byte(0), got[0], "FIFO head survives the overflow")
}
