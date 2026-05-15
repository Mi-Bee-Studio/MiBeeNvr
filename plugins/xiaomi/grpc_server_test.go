// SPDX-License-Identifier: MIT
//
// Tests for PluginServer — validates gRPC PluginService implementation.

package xiaomi

import (
	"context"
	"net"
	"testing"
	"time"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const testBufSize = 1024 * 1024

func setupTestServer(t *testing.T) (*PluginServer, gen.PluginServiceClient, func()) {
	t.Helper()

	srv := NewPluginServer()

	listener := bufconn.Listen(testBufSize)
	grpcServer := grpc.NewServer()
	gen.RegisterPluginServiceServer(grpcServer, srv)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			if err.Error() != "grpc: the server has been stopped" {
				t.Logf("grpc server error: %v", err)
			}
		}
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := gen.NewPluginServiceClient(conn)

	cleanup := func() {
		conn.Close()
		grpcServer.GracefulStop()
	}

	return srv, client, cleanup
}

func TestGetPluginInfo(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	info, err := client.GetPluginInfo(context.Background(), &gen.Empty{})
	require.NoError(t, err)
	require.Equal(t, "xiaomi", info.GetName())
	require.Equal(t, "1.0.0", info.GetVersion())
	require.Equal(t, []string{"xiaomi"}, info.GetProtocols())
	require.NotNil(t, info.GetCapabilities())
	require.True(t, info.GetCapabilities().GetDiscovery())
	require.False(t, info.GetCapabilities().GetHls())
	require.False(t, info.GetCapabilities().GetPtz())
	require.Contains(t, info.GetSupportedEncodings(), gen.Codec_CODEC_H264)
	require.Contains(t, info.GetSupportedEncodings(), gen.Codec_CODEC_H265)
}

func TestHealthCheck(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := client.HealthCheck(context.Background(), &gen.Empty{})
	require.NoError(t, err)
	require.True(t, resp.GetHealthy())
	require.Contains(t, resp.GetMessage(), "xiaomi plugin")
}

func TestSetCloudConfig(t *testing.T) {
	srv, client, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := client.SetCloudConfig(context.Background(), &gen.CloudConfig{
		ServiceToken: "test-token",
		UserId:       "test-user",
		DeviceId:     "test-device",
		Region:       "cn",
	})
	require.NoError(t, err)
	require.True(t, resp.GetSuccess())
	require.Contains(t, resp.GetMessage(), "accepted")

	cfg := srv.GetCloudConfig()
	require.Equal(t, "test-token", cfg.Token)
	require.Equal(t, "test-user", cfg.UserID)
	require.Equal(t, "cn", cfg.Region)
}

func TestGetRecorderStatus_Idle(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "nonexistent"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_IDLE, status.GetState())
}

func TestStartRecorder_SendsFrames(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "test-cam-1",
		Name:     "Test Camera",
		Url:      "xiaomi://12345",
		Options:  map[string]string{"did": "12345"},
	})
	require.NoError(t, err)

	var frames []*gen.Frame
	for {
		frame, err := stream.Recv()
		if err != nil {
			break
		}
		frames = append(frames, frame)
	}

	require.GreaterOrEqual(t, len(frames), 5,
		"should receive at least 5 frames in 2 seconds")
	t.Logf("Received %d frames before context timeout", len(frames))

	require.True(t, frames[0].GetIsCodecInfo())
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].GetCodec())
	require.Contains(t, frames[0].GetExtra(), "sps_hex")
	require.True(t, frames[1].GetIsCodecInfo())
	require.Contains(t, frames[1].GetExtra(), "pps_hex")
	require.True(t, frames[2].GetIsIdr())
}

func TestStartRecorder_StatusWhileRecording(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "status-test",
		Name:     "Status Test Camera",
	})
	require.NoError(t, err)

	_, err = stream.Recv()
	require.NoError(t, err)

	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "status-test"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_RECORDING, status.GetState())
	require.Greater(t, status.GetBytesRecorded(), int64(0))
	require.Greater(t, status.GetUptimeNs(), uint64(0))
}

func TestStartRecorder_StopRecorder(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "test-cam-stop",
		Name:     "Stoppable Camera",
		Url:      "xiaomi://99999",
	})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := stream.Recv()
		require.NoError(t, err, "should receive initial frames")
	}

	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "test-cam-stop"})
	require.NoError(t, err)

	deadline := time.After(2 * time.Second)
	stopped := false
	for !stopped {
		_, err := stream.Recv()
		if err != nil {
			stopped = true
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for stream to close after stop")
		default:
		}
	}

	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "test-cam-stop"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_IDLE, status.GetState())
}

func TestStopRecorder_NotFound(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, err := client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "does-not-exist"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestConcurrentRecorders(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream1, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "cam-1", Name: "Camera 1",
	})
	require.NoError(t, err)

	stream2, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "cam-2", Name: "Camera 2",
	})
	require.NoError(t, err)

	var frames1, frames2 []*gen.Frame
	for len(frames1) < 3 || len(frames2) < 3 {
		if len(frames1) < 3 {
			f, err := stream1.Recv()
			if err == nil {
				frames1 = append(frames1, f)
			}
		}
		if len(frames2) < 3 {
			f, err := stream2.Recv()
			if err == nil {
				frames2 = append(frames2, f)
			}
		}
	}

	require.GreaterOrEqual(t, len(frames1), 3)
	require.GreaterOrEqual(t, len(frames2), 3)

	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "cam-1"})
	require.NoError(t, err)
	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "cam-2"})
	require.NoError(t, err)
}
