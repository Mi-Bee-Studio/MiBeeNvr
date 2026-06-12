package ai

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestJPEG produces a valid JPEG byte slice of the given dimensions
// filled with a deterministic gradient pattern.
func createTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8((x * 255) / width),
				G: uint8((y * 255) / height),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	require.NoError(t, err)
	return buf.Bytes()
}

func TestLetterboxParams(t *testing.T) {
	tests := []struct {
		name       string
		srcW, srcH int
		targetSize int
		wantNewW   int
		wantNewH   int
		wantPadX   int
		wantPadY   int
		wantScale  float64
	}{
		{
			name:       "square image fits exactly",
			srcW:       640, srcH: 640, targetSize: 640,
			wantNewW: 640, wantNewH: 640,
			wantPadX: 0, wantPadY: 0, wantScale: 1.0,
		},
		{
			name:       "landscape image gets pillarbox",
			srcW:       1280, srcH: 720, targetSize: 640,
			wantNewW: 640, wantNewH: 360,
			wantPadX: 0, wantPadY: 140, wantScale: 0.5,
		},
		{
			name:       "portrait image gets letterbox",
			srcW:       720, srcH: 1280, targetSize: 640,
			wantNewW: 360, wantNewH: 640,
			wantPadX: 140, wantPadY: 0, wantScale: 0.5,
		},
		{
			name:       "smaller image is upscaled",
			srcW:       320, srcH: 240, targetSize: 640,
			wantNewW: 640, wantNewH: 480,
			wantPadX: 0, wantPadY: 80, wantScale: 2.0,
		},
		{
			name:       "tall narrow image",
			srcW:       200, srcH: 600, targetSize: 640,
			wantNewW: 213, wantNewH: 640,
			wantPadX: 213, wantPadY: 0, wantScale: 1.0666666666666667,
		},
		{
			name:       "zero dimensions return zeros",
			srcW:       0, srcH: 0, targetSize: 640,
			wantNewW: 0, wantNewH: 0,
			wantPadX: 0, wantPadY: 0, wantScale: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newW, newH, scale, padX, padY := LetterboxParams(tt.srcW, tt.srcH, tt.targetSize)
			assert.Equal(t, tt.wantNewW, newW, "newW mismatch")
			assert.Equal(t, tt.wantNewH, newH, "newH mismatch")
			assert.Equal(t, tt.wantPadX, padX, "padX mismatch")
			assert.Equal(t, tt.wantPadY, padY, "padY mismatch")
			assert.InDelta(t, tt.wantScale, scale, 1e-9, "scale mismatch")
		})
	}
}

func TestNearestNeighborResize(t *testing.T) {
	tests := []struct {
		name         string
		srcW, srcH   int
		newW, newH   int
	}{
		{"same dimensions", 100, 80, 100, 80},
		{"downscale", 200, 150, 100, 75},
		{"upscale", 50, 40, 100, 80},
		{"square output", 320, 240, 640, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jpegBytes := createTestJPEG(t, tt.srcW, tt.srcH)
			img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
			require.NoError(t, err)

			resized := nearestNeighborResize(img, tt.newW, tt.newH)
			assert.NotNil(t, resized)
			assert.Equal(t, tt.newW, resized.Bounds().Dx(), "width mismatch")
			assert.Equal(t, tt.newH, resized.Bounds().Dy(), "height mismatch")

			// Type guaranteed by return signature (*image.NRGBA)
		})
	}
}

func TestPreprocessJPEG_ValidInput(t *testing.T) {
	inputSize := 640
	jpegBytes := createTestJPEG(t, 1280, 720) // 16:9 landscape

	tensor, err := PreprocessJPEG(jpegBytes, inputSize)
	require.NoError(t, err)
	require.NotNil(t, tensor)

	// Verify tensor shape: 3 * inputSize * inputSize
	expectedLen := 3 * inputSize * inputSize // 3 * 640 * 640 = 1,228,800
	assert.Len(t, tensor, expectedLen, "tensor length should be 3*inputSize*inputSize")

	// Verify values are in [0, 1] range
	for i, v := range tensor {
		if v < 0.0 || v > 1.0 {
			t.Fatalf("tensor[%d] = %f, expected in [0, 1]", i, v)
		}
	}

	// Verify CHW layout: first channelSize values should be R channel
	// For a gray-padded image with gradient, R values at edges should be 114/255 (gray padding)
	channelSize := inputSize * inputSize
	topLeftIdx := 0
	// Top-left pixel should be gray padding (since landscape gets pillarbox — 640x360 on 640x640)
	grayVal := float32(letterboxColor) / 255.0
	assert.InDelta(t, grayVal, tensor[topLeftIdx], 0.01, "top-left should be gray padding")

	// Bottom-right corner — might be image content depending on aspect ratio
	bottomRightR := tensor[0*channelSize+channelSize-1]
	assert.GreaterOrEqual(t, bottomRightR, float32(0.0), "bottom-right R should be >= 0")

	// Verify R, G, B channels have different values (image has content, not uniform gray)
	hasContent := false
	for i := 0; i < channelSize; i++ {
		r := tensor[0*channelSize+i]
		g := tensor[1*channelSize+i]
		b := tensor[2*channelSize+i]
		if !(math.Abs(float64(r-grayVal)) < 0.001 &&
			math.Abs(float64(g-grayVal)) < 0.001 &&
			math.Abs(float64(b-grayVal)) < 0.001) {
			hasContent = true
			break
		}
	}
	assert.True(t, hasContent, "tensor should contain non-gray content from the image")
}

func TestPreprocessJPEG_InvalidInput(t *testing.T) {
	t.Run("empty frame", func(t *testing.T) {
		_, err := PreprocessJPEG([]byte{}, 640)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty frame")
	})

	t.Run("random bytes", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42))
		randomBytes := make([]byte, 1024)
		_, err := rng.Read(randomBytes)
		require.NoError(t, err)

		_, err = PreprocessJPEG(randomBytes, 640)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "jpeg decode failed")
	})

	t.Run("invalid input size", func(t *testing.T) {
		jpegBytes := createTestJPEG(t, 100, 80)
		_, err := PreprocessJPEG(jpegBytes, 0)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid input size")

		_, err = PreprocessJPEG(jpegBytes, -1)
		assert.Error(t, err)
	})

	t.Run("nil frame", func(t *testing.T) {
		_, err := PreprocessJPEG(nil, 640)
		assert.Error(t, err)
	})
}

func TestJPEGPreprocessor_Interface(t *testing.T) {
	inputSize := 640
	preproc := NewJPEGPreprocessor(inputSize)
	assert.Equal(t, inputSize, preproc.InputSize)

	// Test that it implements the Preprocessor interface
	var _ Preprocessor = preproc

	// Verify Preprocess works
	jpegBytes := createTestJPEG(t, 1920, 1080)
	tensor, err := preproc.Preprocess(jpegBytes, 1920, 1080, inputSize)
	require.NoError(t, err)
	assert.Len(t, tensor, 3*inputSize*inputSize)
}

func TestPreprocessJPEG_OutputRepeatability(t *testing.T) {
	// Same input should produce identical output
	inputSize := 320 // smaller for faster tests
	jpegBytes := createTestJPEG(t, 640, 480)

	tensor1, err := PreprocessJPEG(jpegBytes, inputSize)
	require.NoError(t, err)

	tensor2, err := PreprocessJPEG(jpegBytes, inputSize)
	require.NoError(t, err)

	assert.Equal(t, tensor1, tensor2, "same input should produce identical tensor")
}

func TestPreprocessJPEG_VariousAspectRatios(t *testing.T) {
	inputSize := 320

	aspectRatios := []struct {
		name           string
		width, height  int
	}{
		{"square 1:1", 320, 320},
		{"landscape 16:9", 640, 360},
		{"portrait 9:16", 360, 640},
		{"wide 21:9", 630, 270},
		{"ultrawide 32:9", 640, 180},
	}

	for _, ar := range aspectRatios {
		t.Run(ar.name, func(t *testing.T) {
			jpegBytes := createTestJPEG(t, ar.width, ar.height)
			tensor, err := PreprocessJPEG(jpegBytes, inputSize)
			require.NoError(t, err)
			assert.Len(t, tensor, 3*inputSize*inputSize)

			// Verify all values are in [0, 1]
			for _, v := range tensor {
				assert.GreaterOrEqual(t, v, float32(0.0))
				assert.LessOrEqual(t, v, float32(1.0))
			}
		})
	}
}
