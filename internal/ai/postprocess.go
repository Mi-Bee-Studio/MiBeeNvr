package ai

import (
	"fmt"
	"math"
	"sort"
)

// COCOLabels is the standard 80-class COCO label list used by YOLOv11.
var COCOLabels = []string{
	"person", "bicycle", "car", "motorcycle", "airplane", "bus", "train", "truck", "boat",
	"traffic light", "fire hydrant", "stop sign", "parking meter", "bench", "bird", "cat",
	"dog", "horse", "sheep", "cow", "elephant", "bear", "zebra", "giraffe", "backpack",
	"umbrella", "handbag", "tie", "suitcase", "frisbee", "skis", "snowboard", "sports ball",
	"kite", "baseball bat", "baseball glove", "skateboard", "surfboard", "tennis racket",
	"bottle", "wine glass", "cup", "fork", "knife", "spoon", "bowl", "banana", "apple",
	"sandwich", "orange", "broccoli", "carrot", "hot dog", "pizza", "donut", "cake",
	"chair", "couch", "potted plant", "bed", "dining table", "toilet", "tv", "laptop",
	"mouse", "remote", "keyboard", "cell phone", "microwave", "oven", "toaster", "sink",
	"refrigerator", "book", "clock", "vase", "scissors", "teddy bear", "hair drier", "toothbrush",
}

// YOLOPostprocessor implements the Postprocessor interface for YOLOv11-nano models.
type YOLOPostprocessor struct {
	NumClasses    int
	NumBoxCoords  int
	ConfThreshold float64
	IoUThreshold  float64
	Labels        []string
}

// NewYOLOPostprocessor creates a new YOLOPostprocessor with the given thresholds.
func NewYOLOPostprocessor(threshold, iouThreshold float64) *YOLOPostprocessor {
	return &YOLOPostprocessor{
		NumClasses:    80,
		NumBoxCoords:  4,
		ConfThreshold: threshold,
		IoUThreshold:  iouThreshold,
		Labels:        COCOLabels,
	}
}

// Postprocess implements the Postprocessor interface.
// It parses the raw YOLOv11 output tensor, applies confidence filtering,
// and runs Non-Maximum Suppression to produce the final detection list.
func (p *YOLOPostprocessor) Postprocess(rawOutput [][]float32, dims []int64, inputSize int, threshold float64) ([]Detection, error) {
	if len(rawOutput) == 0 {
		return nil, fmt.Errorf("ai: empty raw output")
	}
	data := rawOutput[0]
	if len(data) == 0 {
		return nil, fmt.Errorf("ai: empty output tensor")
	}

	// Use the provided threshold or fall back to the default.
	confThresh := threshold
	if confThresh <= 0 {
		confThresh = p.ConfThreshold
	}

	return ParseYOLOOutput(rawOutput, p.NumClasses, inputSize, confThresh, p.IoUThreshold, p.Labels)
}

// ParseYOLOOutput parses a raw YOLOv11 output tensor into Detection structs.
//
// rawOutput layout: [1, 84, 8400] where 84 = 4 box coords + 80 class scores
// (batch=1, channels=84, anchors=8400) in NCHW row-major format.
// The function transposes to per-anchor format: for each anchor (8400 total)
// it extracts 84 continuous values (cx, cy, w, h + 80 class logits), applies
// sigmoid activation to class scores, converts boxes to x1,y1,x2,y2 in
// normalized coordinates, filters by confidence threshold, and applies NMS.
func ParseYOLOOutput(rawOutput [][]float32, numClasses, inputSize int, confThresh, iouThresh float64, labels []string) ([]Detection, error) {
	if len(rawOutput) == 0 || len(rawOutput[0]) == 0 {
		return nil, fmt.Errorf("ai: empty raw output")
	}
	data := rawOutput[0]

	numChannels := 4 + numClasses // 84 for COCO
	numAnchors := len(data) / numChannels
	if len(data)%numChannels != 0 {
		return nil, fmt.Errorf("ai: output tensor length %d is not divisible by %d channels", len(data), numChannels)
	}

	// Parse raw detections from NCHW tensor.
	// Memory layout (row-major): index = batch*C*H*W + c*numAnchors + a
	// For each anchor a, the 84 channel values are strided by numAnchors.
	detections := make([]Detection, 0, 32)
	for a := 0; a < numAnchors; a++ {
		// Box coordinates (absolute pixels in input space).
		cx := float64(data[0*numAnchors+a])
		cy := float64(data[1*numAnchors+a])
		w := float64(data[2*numAnchors+a])
		h := float64(data[3*numAnchors+a])

		// Skip invalid boxes.
		if w <= 0 || h <= 0 {
			continue
		}

		// Find the highest scoring class.
		maxScore := -1.0
		maxClass := 0
		for c := 0; c < numClasses; c++ {
			rawScore := float64(data[(4+c)*numAnchors+a])
			score := sigmoid(rawScore)
			if score > maxScore {
				maxScore = score
				maxClass = c
			}
		}

		if maxScore < confThresh {
			continue
		}

		// Convert cx, cy, w, h → x1, y1, x2, y2 and normalize to [0, 1].
		x1 := (cx - w/2) / float64(inputSize)
		y1 := (cy - h/2) / float64(inputSize)
		x2 := (cx + w/2) / float64(inputSize)
		y2 := (cy + h/2) / float64(inputSize)

		label := ""
		if maxClass >= 0 && maxClass < len(labels) {
			label = labels[maxClass]
		}

		detections = append(detections, Detection{
			BBox:       [4]float64{x1, y1, x2, y2},
			Confidence: maxScore,
			ClassID:    maxClass,
			ClassLabel: label,
		})
	}

	return NMS(detections, iouThresh), nil
}

// NMS performs Non-Maximum Suppression on a list of detections.
// It sorts by confidence descending, then iteratively picks the highest-confidence
// detection and suppresses all others with IoU above the given threshold.
func NMS(detections []Detection, iouThreshold float64) []Detection {
	if len(detections) == 0 {
		return nil
	}

	sorted := make([]Detection, len(detections))
	copy(sorted, detections)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})

	kept := make([]Detection, 0, len(sorted))
	for len(sorted) > 0 {
		best := sorted[0]
		kept = append(kept, best)

		remaining := make([]Detection, 0, len(sorted)-1)
		for i := 1; i < len(sorted); i++ {
			if calculateIoU(best, sorted[i]) <= iouThreshold {
				remaining = append(remaining, sorted[i])
			}
		}
		sorted = remaining
	}

	return kept
}

// sigmoid applies the logistic sigmoid activation function.
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// calculateIoU computes the Intersection over Union of two bounding boxes.
// Boxes are in normalized [x1, y1, x2, y2] format.
func calculateIoU(a, b Detection) float64 {
	ax1, ay1, ax2, ay2 := a.BBox[0], a.BBox[1], a.BBox[2], a.BBox[3]
	bx1, by1, bx2, by2 := b.BBox[0], b.BBox[1], b.BBox[2], b.BBox[3]

	interX1 := math.Max(ax1, bx1)
	interY1 := math.Max(ay1, by1)
	interX2 := math.Min(ax2, bx2)
	interY2 := math.Min(ay2, by2)

	interW := math.Max(0, interX2-interX1)
	interH := math.Max(0, interY2-interY1)
	interArea := interW * interH

	areaA := (ax2 - ax1) * (ay2 - ay1)
	areaB := (bx2 - bx1) * (by2 - by1)

	if areaA <= 0 || areaB <= 0 {
		return 0
	}

	return interArea / (areaA + areaB - interArea)
}
