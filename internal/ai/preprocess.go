package ai

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"
)

// letterboxColor is the gray padding value used for letterbox borders.
// Standard YOLO practice uses 114 (matches frontend LETTERBOX_COLOR).
const letterboxColor = 114

// LetterboxParams calculates scale and padding to fit srcW×srcH into
// targetSize×targetSize while maintaining aspect ratio.
//
// Returns the new dimensions, scale factor, and padding offsets.
func LetterboxParams(srcW, srcH, targetSize int) (newW, newH int, scale float64, padX, padY int) {
	if srcW <= 0 || srcH <= 0 || targetSize <= 0 {
		return 0, 0, 0, 0, 0
	}
	scale = math.Min(float64(targetSize)/float64(srcW), float64(targetSize)/float64(srcH))
	newW = int(math.Round(float64(srcW) * scale))
	newH = int(math.Round(float64(srcH) * scale))
	padX = (targetSize - newW) / 2
	padY = (targetSize - newH) / 2
	return
}

// nearestNeighborResize resizes src to newW×newH using nearest-neighbor
// interpolation. This is the fastest resize method suitable for YOLO
// preprocessing where pixel-perfect accuracy is not critical.
//
// Returns a new *image.NRGBA with the requested dimensions.
func nearestNeighborResize(src image.Image, newW, newH int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	xRatio := float64(srcW) / float64(newW)
	yRatio := float64(srcH) / float64(newH)

	dstStride := dst.Stride
	for dy := 0; dy < newH; dy++ {
		sy := bounds.Min.Y + int(float64(dy)*yRatio)
		for dx := 0; dx < newW; dx++ {
			sx := bounds.Min.X + int(float64(dx)*xRatio)
			r, g, b, a := src.At(sx, sy).RGBA()
			offset := dy*dstStride + dx*4
			dst.Pix[offset+0] = uint8(r >> 8)
			dst.Pix[offset+1] = uint8(g >> 8)
			dst.Pix[offset+2] = uint8(b >> 8)
			dst.Pix[offset+3] = uint8(a >> 8)
		}
	}

	return dst
}

// PreprocessJPEG decodes a JPEG frame, letterboxes it to inputSize×inputSize,
// and returns a flat CHW (RGB) float32 tensor normalized to [0, 1].
//
// Pipeline:
//  1. Decode JPEG bytes to image.Image using stdlib image/jpeg
//  2. Calculate letterbox parameters (scale, padding)
//  3. Nearest-neighbor resize to fit within target size
//  4. Place resized image onto gray-padded canvas (letterbox)
//  5. Convert RGBA canvas to CHW float32 tensor (R, G, B channels)
//
// Output shape: [3, inputSize, inputSize] as flat []float32.
func PreprocessJPEG(frame []byte, inputSize int) ([]float32, error) {
	if len(frame) == 0 {
		return nil, fmt.Errorf("ai: empty frame")
	}
	if inputSize <= 0 {
		return nil, fmt.Errorf("ai: invalid input size %d", inputSize)
	}

	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		return nil, fmt.Errorf("ai: jpeg decode failed: %w", err)
	}

	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("ai: decoded image has zero dimensions (%dx%d)", srcW, srcH)
	}

	newW, newH, _, padX, padY := LetterboxParams(srcW, srcH, inputSize)

	// Nearest-neighbor resize
	resized := nearestNeighborResize(img, newW, newH)

	// Create letterbox canvas
	canvas := image.NewNRGBA(image.Rect(0, 0, inputSize, inputSize))
	canvasStride := canvas.Stride

	// Fill canvas with gray padding using direct Pix access
	gray := uint8(letterboxColor)
	for i := 0; i < inputSize*canvasStride; i += 4 {
		canvas.Pix[i+0] = gray
		canvas.Pix[i+1] = gray
		canvas.Pix[i+2] = gray
		canvas.Pix[i+3] = 255
	}

	// Copy resized image onto canvas at (padX, padY)
	resizedStride := resized.Stride
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			srcOff := y*resizedStride + x*4
			dstOff := (padY+y)*canvasStride + (padX+x)*4
			canvas.Pix[dstOff+0] = resized.Pix[srcOff+0]
			canvas.Pix[dstOff+1] = resized.Pix[srcOff+1]
			canvas.Pix[dstOff+2] = resized.Pix[srcOff+2]
			canvas.Pix[dstOff+3] = resized.Pix[srcOff+3]
		}
	}

	// Convert to CHW float32 tensor (RGB order)
	channelSize := inputSize * inputSize
	tensor := make([]float32, 3*channelSize)

	for y := 0; y < inputSize; y++ {
		for x := 0; x < inputSize; x++ {
			offset := y*canvasStride + x*4
			idx := y*inputSize + x
			tensor[0*channelSize+idx] = float32(canvas.Pix[offset+0]) / 255.0 // R
			tensor[1*channelSize+idx] = float32(canvas.Pix[offset+1]) / 255.0 // G
			tensor[2*channelSize+idx] = float32(canvas.Pix[offset+2]) / 255.0 // B
		}
	}

	return tensor, nil
}

// JPEGPreprocessor implements the Preprocessor interface (from engine.go)
// for JPEG-encoded frame sources. It decodes JPEG bytes, letterboxes,
// and converts to normalized CHW float32 tensors.
type JPEGPreprocessor struct {
	InputSize int
}

// NewJPEGPreprocessor creates a new JPEGPreprocessor with the given input size.
// Typical YOLO models use inputSize=640.
func NewJPEGPreprocessor(inputSize int) *JPEGPreprocessor {
	return &JPEGPreprocessor{InputSize: inputSize}
}

// Preprocess decodes a JPEG frame and converts it to a normalized CHW float32
// tensor. The width and height parameters are unused for JPEG preprocessing
// (dimensions are extracted from the decoded image).
func (p *JPEGPreprocessor) Preprocess(frame []byte, width, height, inputSize int) ([]float32, error) {
	return PreprocessJPEG(frame, inputSize)
}
