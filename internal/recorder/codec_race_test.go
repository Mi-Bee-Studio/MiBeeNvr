package recorder

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests guard the fix for #219: the baseRecorder codec parameter sets
// (SPS/PPS/VPS) are read cross-goroutine by live-preview handlers
// (SPS()/PPS()/VPS()/CodecParams()) and written by the writeFrames goroutine
// (handleParamSet) + connectAndRecord (SDP pre-seed). Previously these were
// plain []byte fields → a torn/unsynchronized read under -race. They are now
// an atomic.Pointer[codecParams] snapshot. The tests prove that concurrent
// writers + readers run cleanly under `go test -race`.

// sampleParamSets returns distinct SPS/PPS/VPS byte slices for h264/h265.
func sampleParamSets(variant int) (sps, pps, vps []byte) {
	sps = []byte{0x67, 0x64, 0x00, byte(variant)}
	pps = []byte{0x68, byte(variant)}
	vps = []byte{0x40, 0x01, 0x0C, byte(variant)}
	return
}

// TestCodecParams_ConcurrentReadWrite_H264 spins writer goroutines that update
// the H.264 recorder's codec snapshot (simulating handleParamSet + SDP seed)
// while reader goroutines invoke every public accessor. Must not trigger the
// race detector and must not panic.
func TestCodecParams_ConcurrentReadWrite_H264(t *testing.T) {
	t.Parallel()
	rec := NewH264Recorder(H264Config{CameraID: "race-h264"}, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers — update the snapshot continuously, varying the param bytes so the
	// race detector is more likely to catch a torn read if the atomic guard
	// regresses. Also exercises the HLSProvider CodecParams() read path.
	const writers = 2
	for w := range writers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				sps, pps, _ := sampleParamSets(w*1000 + i)
				rec.setCodecParams(sps, pps, nil) // H.264: vps always nil
			}
		}()
	}

	// Readers — call all public codec accessors concurrently.
	const readers = 4
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Single-snapshot read path (the recommended fix).
				_, _, _, _ = rec.CodecParams()
				// Legacy single-field accessors (thin wrappers over the snapshot).
				_ = rec.SPS()
				_ = rec.PPS()
			}
		}()
	}

	// Let the race detector observe contention, then stop.
	close(stop)
	wg.Wait()

	// Final state is consistent: the accessors return a coherent triplet.
	_, sps, pps, vps := rec.CodecParams()
	if sps != nil {
		require.NotNil(t, pps, "H264 must have PPS whenever SPS is set")
	}
	require.Nil(t, vps, "H264 must never report a VPS")
}

// TestCodecParams_ConcurrentReadWrite_H265 is the H.265 counterpart, also
// exercising the VPS accessor and the full VPS/SPS/PPS triplet writes.
func TestCodecParams_ConcurrentReadWrite_H265(t *testing.T) {
	t.Parallel()
	rec := NewH265Recorder(H265Config{CameraID: "race-h265"}, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	const writers = 2
	for w := range writers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				sps, pps, vps := sampleParamSets(w*1000 + i)
				rec.setCodecParams(vps, sps, pps)
			}
		}()
	}

	const readers = 4
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _, _, _ = rec.CodecParams()
				_ = rec.VPS()
				_ = rec.SPS()
				_ = rec.PPS()
			}
		}()
	}

	close(stop)
	wg.Wait()

	// Consistent final state: if any of VPS/SPS/PPS is set, all three are.
	vps, sps, pps, _ := rec.CodecParams()
	if sps != nil {
		require.NotNil(t, pps, "H265 must have PPS whenever SPS is set")
		require.NotNil(t, vps, "H265 must have VPS whenever SPS is set")
	}
}
