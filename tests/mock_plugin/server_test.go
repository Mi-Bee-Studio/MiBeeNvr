package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

const bufSize = 1024 * 1024 // 1MB buffer for in-memory gRPC

func setupTestServer(t *testing.T) (*MockServer, gen.PluginServiceClient, func()) {
	t.Helper()

	srv := NewMockServer()

	listener := bufconn.Listen(bufSize)
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

func assertFrameSequence(t *testing.T, frames []*gen.Frame, minCount int) {
	t.Helper()
	require.GreaterOrEqual(t, len(frames), minCount,
		"should receive at least %d frames", minCount)

	if len(frames) < 3 {
		return
	}

	// First frame should be SPS (codec info)
	require.True(t, frames[0].GetIsCodecInfo(), "first frame should be SPS codec info")
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].GetCodec())
	require.Contains(t, frames[0].GetExtra(), "sps_hex")

	// Second frame should be PPS (codec info)
	require.True(t, frames[1].GetIsCodecInfo(), "second frame should be PPS codec info")
	require.Contains(t, frames[1].GetExtra(), "pps_hex")

	// Third frame should be IDR
	require.True(t, frames[2].GetIsIdr(), "third frame should be IDR")
	require.False(t, frames[2].GetIsCodecInfo(), "IDR should not be codec info")

	// Check that every 30th media frame is an IDR (offset by 2 for SPS/PPS)
	for i := 2; i < len(frames); i++ {
		isExpectedIDR := (i-2)%30 == 0
		if isExpectedIDR {
			require.True(t, frames[i].GetIsIdr(),
				"frame %d should be IDR (every 30th frame)", i)
		}
	}
}

func TestGetPluginInfo(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	info, err := client.GetPluginInfo(context.Background(), &gen.Empty{})
	require.NoError(t, err)
	require.Equal(t, "mock", info.GetName())
	require.Equal(t, "0.1.0", info.GetVersion())
	require.Equal(t, []string{"mock"}, info.GetProtocols())
	require.NotNil(t, info.GetCapabilities())
	require.False(t, info.GetCapabilities().GetHls())
	require.False(t, info.GetCapabilities().GetPtz())
	require.False(t, info.GetCapabilities().GetSnapshot())
	require.False(t, info.GetCapabilities().GetDiscovery())
	require.Contains(t, info.GetSupportedEncodings(), gen.Codec_CODEC_H264)
}

func TestStartRecorder_SendsFrames(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId:          "test-cam-1",
		Name:              "Test Camera",
		Url:               "mock://test",
		SegmentDurationNs: uint64(1 * time.Second.Nanoseconds()),
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
		"should receive at least 5 frames (SPS, PPS, IDR, P, P) in 2 seconds")
	t.Logf("Received %d frames before context timeout", len(frames))
}

func TestStartRecorder_StopRecorder(t *testing.T) {
	srv, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Start recording
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "test-cam-stop",
		Name:     "Stoppable Camera",
		Url:      "mock://stop-test",
	})
	require.NoError(t, err)

	// Wait for a few frames
	var initialFrames []*gen.Frame
	for i := 0; i < 5; i++ {
		frame, err := stream.Recv()
		require.NoError(t, err, "should receive initial frames")
		initialFrames = append(initialFrames, frame)
	}
	assertFrameSequence(t, initialFrames, 3)

	// Now stop the recorder
	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "test-cam-stop"})
	require.NoError(t, err)

	// The stream should close shortly after stop
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

	// Status should show IDLE (recorder was removed from map after stop)
	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "test-cam-stop"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_IDLE, status.GetState())

	_ = srv
}

func TestGetRecorderStatus_Idle(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "nonexistent"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_IDLE, status.GetState())
}

func TestGetRecorderStatus_Recording(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "status-test",
		Name:     "Status Test Camera",
	})
	require.NoError(t, err)

	// Wait for first frame (ensures recording has started)
	_, err = stream.Recv()
	require.NoError(t, err)

	// Check status while recording
	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "status-test"})
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_RECORDING, status.GetState())
	require.Greater(t, status.GetBytesRecorded(), int64(0), "should have recorded some bytes")
	require.Greater(t, status.GetUptimeNs(), uint64(0), "uptime should be positive")
}

func TestHealthCheck(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := client.HealthCheck(context.Background(), &gen.Empty{})
	require.NoError(t, err)
	require.True(t, resp.GetHealthy())
	require.Contains(t, resp.GetMessage(), "mock plugin")
}

func TestSetCloudConfig(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
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
}

func TestStartRecorder_StartError(t *testing.T) {
	srv, client, cleanup := setupTestServer(t)
	defer cleanup()

	srv.StartError = io.ErrUnexpectedEOF

	stream, err := client.StartRecorder(context.Background(), &gen.RecorderConfig{
		CameraId: "error-test",
	})
	require.NoError(t, err) // initial call succeeds, error on first Recv

	_, err = stream.Recv()
	require.Error(t, err) // should receive the injected error
}

func TestFrameSequence_ProducesCorrectNAL(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "nal-test",
		Name:     "NAL Structure Test",
	})
	require.NoError(t, err)

	var frames []*gen.Frame
	for len(frames) < 35 {
		frame, err := stream.Recv()
		if err != nil {
			break
		}
		frames = append(frames, frame)
	}

	assertFrameSequence(t, frames, 35)

	// Check NAL start code prefix on all frames
	for i, f := range frames {
		require.GreaterOrEqual(t, len(f.GetData()), 5,
			"frame %d data too short", i)
		require.Equal(t, []byte{0x00, 0x00, 0x00, 0x01}, f.GetData()[:4],
			"frame %d missing Annex B start code", i)

		if !f.GetIsCodecInfo() {
			nalType := f.GetData()[4] & 0x1F
			if f.GetIsIdr() {
				require.Equal(t, uint8(5), nalType,
					"IDR frame %d should have NAL type 5, got %d", i, nalType)
			} else {
				require.Equal(t, uint8(1), nalType,
					"P-frame %d should have NAL type 1, got %d", i, nalType)
			}
		}
	}
}

func TestConcurrentRecorders(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start two recorders concurrently
	stream1, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "cam-1", Name: "Camera 1",
	})
	require.NoError(t, err)

	stream2, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "cam-2", Name: "Camera 2",
	})
	require.NoError(t, err)

	// Both should produce frames — collect 3 from each
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

	require.GreaterOrEqual(t, len(frames1), 3, "cam-1 should produce frames")
	require.GreaterOrEqual(t, len(frames2), 3, "cam-2 should produce frames")

	// Stop both
	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "cam-1"})
	require.NoError(t, err)
	_, err = client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "cam-2"})
	require.NoError(t, err)
}

func TestStopRecorder_NotFound(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, err := client.StopRecorder(context.Background(), &gen.StopRequest{CameraId: "does-not-exist"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestRecorderBytesAndSegments(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "metrics-test",
		Name:     "Metrics Test",
	})
	require.NoError(t, err)

	// Read some frames
	for i := 0; i < 10; i++ {
		_, err := stream.Recv()
		require.NoError(t, err)
	}

	status, err := client.GetRecorderStatus(context.Background(), &gen.StatusRequest{CameraId: "metrics-test"})
	require.NoError(t, err)
	require.Greater(t, status.GetBytesRecorded(), int64(0), "should have bytes recorded")
	require.Equal(t, int64(0), status.GetSegmentsCreated(),
		"no segment should complete within 10 frames (needs 30)")
}

func TestStartRecorder_ReturnsSPSandPPSInExtra(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.StartRecorder(ctx, &gen.RecorderConfig{
		CameraId: "sps-pps-test",
	})
	require.NoError(t, err)

	// First frame should be SPS with extra
	spsFrame, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, spsFrame.GetIsCodecInfo())
	require.Equal(t, "6742c01ed90000", spsFrame.GetExtra()["sps_hex"])

	// Second frame should be PPS with extra
	ppsFrame, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, ppsFrame.GetIsCodecInfo())
	require.Equal(t, "68ce3880", ppsFrame.GetExtra()["pps_hex"])
}
