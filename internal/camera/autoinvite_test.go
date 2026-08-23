package camera

import (
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fakeInviterEnder records the recycle + INVITE sequence fired by
// autoInviteGB28181, mutex-guarded because the helper runs detached.
type fakeInviterEnder struct {
	mu      sync.Mutex
	byes    []string
	invites []string
}

func (f *fakeInviterEnder) ByeChannelByID(channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byes = append(f.byes, channelID)
	return nil
}

func (f *fakeInviterEnder) InviteChannel(deviceID, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invites = append(f.invites, deviceID+"/"+channelID)
	return nil
}

// TestAutoInviteGB28181: starting a GB28181 recorder must recycle the
// channel's live session (its AU callback may point at a dead recorder or an
// orphan hub) and re-INVITE so media binds the fresh instance.
func TestAutoInviteGB28181(t *testing.T) {
	t.Parallel()

	fe := &fakeInviterEnder{}
	cm := &CameraManager{}
	cm.SetGB28181Inviter(fe)
	cm.SetGB28181SessionEnder(fe)

	cm.autoInviteGB28181(config.CameraConfig{
		ID:       "gb-x",
		Protocol: string(model.ProtoGB28181),
		GB28181: config.GB28181ChannelConfig{
			DeviceID:  "34020000012000000152",
			ChannelID: "34020000011320000003",
		},
	})

	require.Eventually(t, func() bool {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		return len(fe.invites) == 1
	}, 2*time.Second, 10*time.Millisecond, "INVITE should fire after Bye")

	fe.mu.Lock()
	defer fe.mu.Unlock()
	require.Equal(t, []string{"34020000011320000003"}, fe.byes, "session must be recycled before the INVITE")
	require.Equal(t, []string{"34020000012000000152/34020000011320000003"}, fe.invites)
}
