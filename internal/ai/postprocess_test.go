package ai

import (
	"math"
	"testing"
)

// closeEnough checks if two float64 values are within a relative tolerance.
func closeEnough(t testing.TB, a, b, tol float64) bool {
	t.Helper()
	if a == b {
		return true
	}
	diff := math.Abs(a - b)
	avg := (math.Abs(a) + math.Abs(b)) / 2
	if avg == 0 {
		return diff < tol
	}
	return diff/avg < tol
}

// closeBox checks if two [4]float64 boxes are element-wise within tolerance.
func closeBox(t testing.TB, a, b [4]float64, tol float64) bool {
	t.Helper()
	for i := range a {
		if !closeEnough(t, a[i], b[i], tol) {
			return false
		}
	}
	return true
}

func TestParseYOLOOutput_SyntheticData(t *testing.T) {
	numClasses := 80
	numChannels := 4 + numClasses // 84
	numAnchors := 8400
	inputSize := 640
	confThresh := 0.8
	iouThresh := 0.45

	// Build synthetic [1, 84, 8400] NCHW tensor.
	data := make([]float32, numChannels*numAnchors)

	// Place a clear detection at anchor 50.
	anchor := 50
	data[0*numAnchors+anchor] = 320 // cx
	data[1*numAnchors+anchor] = 240 // cy
	data[2*numAnchors+anchor] = 100 // w
	data[3*numAnchors+anchor] = 150 // h
	data[4*numAnchors+anchor] = 10  // class 0 logit (sigmoid ≈ 0.99995)

	// All other anchors stay at zero → w=0, h=0 → filtered by shape check.
	// All other class scores stay at 0 → sigmoid(0) = 0.5 → filtered by threshold.

	rawOutput := [][]float32{data}
	dets, err := ParseYOLOOutput(rawOutput, numClasses, inputSize, confThresh, iouThresh, COCOLabels)
	if err != nil {
		t.Fatalf("ParseYOLOOutput returned error: %v", err)
	}

	if len(dets) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(dets))
	}

	d := dets[0]

	// Expected normalized box: cx=320,cy=240,w=100,h=150
	// x1 = (320-50)/640 = 270/640 = 0.421875
	// y1 = (240-75)/640 = 165/640 = 0.2578125
	// x2 = (320+50)/640 = 370/640 = 0.578125
	// y2 = (240+75)/640 = 315/640 = 0.4921875
	expectedBBox := [4]float64{0.421875, 0.2578125, 0.578125, 0.4921875}
	if !closeBox(t, d.BBox, expectedBBox, 1e-6) {
		t.Errorf("bbox: got %v, want %v", d.BBox, expectedBBox)
	}

	// Expected confidence: sigmoid(10) ≈ 0.9999546
	expectedConf := sigmoid(10)
	if !closeEnough(t, d.Confidence, expectedConf, 1e-6) {
		t.Errorf("confidence: got %v, want %v", d.Confidence, expectedConf)
	}

	if d.ClassID != 0 {
		t.Errorf("class_id: got %d, want 0", d.ClassID)
	}
	if d.ClassLabel != "person" {
		t.Errorf("class_label: got %q, want %q", d.ClassLabel, "person")
	}
}

func TestParseYOLOOutput_EmptyRawOutput(t *testing.T) {
	_, err := ParseYOLOOutput([][]float32{}, 80, 640, 0.5, 0.45, COCOLabels)
	if err == nil {
		t.Fatal("expected error for empty raw output")
	}
}

func TestParseYOLOOutput_NoDetections(t *testing.T) {
	numClasses := 80
	numChannels := 4 + numClasses
	numAnchors := 8400

	// All zeros → all boxes have w=0, h=0 → no detections.
	data := make([]float32, numChannels*numAnchors)
	rawOutput := [][]float32{data}

	dets, err := ParseYOLOOutput(rawOutput, numClasses, 640, 0.5, 0.45, COCOLabels)
	if err != nil {
		t.Fatalf("ParseYOLOOutput returned error: %v", err)
	}
	if len(dets) != 0 {
		t.Errorf("expected 0 detections, got %d", len(dets))
	}
}

func TestParseYOLOOutput_InvalidTensorLength(t *testing.T) {
	// Tensor length not divisible by channels.
	data := make([]float32, 100) // not multiple of 84
	rawOutput := [][]float32{data}
	_, err := ParseYOLOOutput(rawOutput, 80, 640, 0.5, 0.45, COCOLabels)
	if err == nil {
		t.Fatal("expected error for invalid tensor length")
	}
}

func TestNMS_OverlappingBoxes(t *testing.T) {
	// Three overlapping person boxes with IoU > 0.45.
	// Box1: [0.1, 0.1, 0.5, 0.5] (confidence 0.9)
	// Box2: [0.15, 0.15, 0.55, 0.55] (confidence 0.8, IoU~0.62)
	// Box3: [0.12, 0.12, 0.52, 0.52] (confidence 0.7, IoU~0.74)
	dets := []Detection{
		{BBox: [4]float64{0.1, 0.1, 0.5, 0.5}, Confidence: 0.9, ClassID: 0, ClassLabel: "person"},
		{BBox: [4]float64{0.15, 0.15, 0.55, 0.55}, Confidence: 0.8, ClassID: 0, ClassLabel: "person"},
		{BBox: [4]float64{0.12, 0.12, 0.52, 0.52}, Confidence: 0.7, ClassID: 0, ClassLabel: "person"},
	}

	result := NMS(dets, 0.45)
	if len(result) != 1 {
		t.Fatalf("expected 1 detection after NMS, got %d", len(result))
	}
	if result[0].Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %f", result[0].Confidence)
	}
}

func TestNMS_OverlappingBoxes_HighThreshold(t *testing.T) {
	// With a high IoU threshold (0.95), overlapping boxes should be kept.
	dets := []Detection{
		{BBox: [4]float64{0.1, 0.1, 0.5, 0.5}, Confidence: 0.9, ClassID: 0},
		{BBox: [4]float64{0.15, 0.15, 0.55, 0.55}, Confidence: 0.8, ClassID: 0},
	}

	result := NMS(dets, 0.95)
	if len(result) != 2 {
		t.Fatalf("expected 2 detections with high IoU threshold, got %d", len(result))
	}
}

func TestNMS_NoOverlap(t *testing.T) {
	dets := []Detection{
		{BBox: [4]float64{0.0, 0.0, 0.1, 0.1}, Confidence: 0.9, ClassID: 0},
		{BBox: [4]float64{0.2, 0.2, 0.3, 0.3}, Confidence: 0.8, ClassID: 1},
		{BBox: [4]float64{0.5, 0.5, 0.6, 0.6}, Confidence: 0.7, ClassID: 2},
	}

	result := NMS(dets, 0.45)
	if len(result) != 3 {
		t.Fatalf("expected 3 detections (no overlap), got %d", len(result))
	}
	// Verify they remain in confidence order.
	if result[0].Confidence != 0.9 || result[1].Confidence != 0.8 || result[2].Confidence != 0.7 {
		t.Errorf("detections not in confidence order: %v", result)
	}
}

func TestNMS_Empty(t *testing.T) {
	result := NMS(nil, 0.45)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}

	result = NMS([]Detection{}, 0.45)
	if result != nil {
		t.Errorf("expected nil for empty slice, got %v", result)
	}
}

func TestNMS_Single(t *testing.T) {
	dets := []Detection{
		{BBox: [4]float64{0.1, 0.1, 0.5, 0.5}, Confidence: 0.9, ClassID: 0},
	}
	result := NMS(dets, 0.45)
	if len(result) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(result))
	}
}

func TestSigmoid(t *testing.T) {
	tests := []struct {
		input    float64
		expected float64
	}{
		{0, 0.5},
		{10, 0.9999546021312976},
		{-10, 0.00004539786870243435},
		{100, 1.0},
		{-100, 3.7200759760208356e-44},
		{1, 0.7310585786300049},
		{-1, 0.2689414213699951},
	}

	for _, tt := range tests {
		got := sigmoid(tt.input)
		if !closeEnough(t, got, tt.expected, 1e-10) {
			t.Errorf("sigmoid(%v) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestCalculateIoU_Identical(t *testing.T) {
	a := Detection{BBox: [4]float64{0.1, 0.1, 0.5, 0.5}}
	b := Detection{BBox: [4]float64{0.1, 0.1, 0.5, 0.5}}

	iou := calculateIoU(a, b)
	if !closeEnough(t, iou, 1.0, 1e-10) {
		t.Errorf("IoU of identical boxes: got %v, want 1.0", iou)
	}
}

func TestCalculateIoU_NonOverlapping(t *testing.T) {
	a := Detection{BBox: [4]float64{0.0, 0.0, 0.1, 0.1}}
	b := Detection{BBox: [4]float64{0.5, 0.5, 0.6, 0.6}}

	iou := calculateIoU(a, b)
	if !closeEnough(t, iou, 0.0, 1e-10) {
		t.Errorf("IoU of non-overlapping boxes: got %v, want 0.0", iou)
	}
}

func TestCalculateIoU_PartialOverlap(t *testing.T) {
	// Box A: [0, 0, 1, 1], Area = 1
	// Box B: [0.5, 0, 1.5, 1], Area = 1
	// Inter: [0.5, 0, 1, 1], Area = 0.5
	// IoU = 0.5 / (1 + 1 - 0.5) = 0.5 / 1.5 = 1/3 ≈ 0.333
	a := Detection{BBox: [4]float64{0.0, 0.0, 1.0, 1.0}}
	b := Detection{BBox: [4]float64{0.5, 0.0, 1.5, 1.0}}

	iou := calculateIoU(a, b)
	expected := 1.0 / 3.0
	if !closeEnough(t, iou, expected, 1e-10) {
		t.Errorf("IoU: got %v, want %v", iou, expected)
	}
}

func TestCalculateIoU_ZeroArea(t *testing.T) {
	a := Detection{BBox: [4]float64{0.0, 0.0, 0.0, 1.0}} // zero width
	b := Detection{BBox: [4]float64{0.0, 0.0, 1.0, 1.0}}

	iou := calculateIoU(a, b)
	if !closeEnough(t, iou, 0.0, 1e-10) {
		t.Errorf("IoU with zero-area box: got %v, want 0.0", iou)
	}
}

func TestYOLOPostprocessor_Postprocess(t *testing.T) {
	pp := NewYOLOPostprocessor(0.5, 0.45)

	numClasses := 80
	numChannels := 4 + numClasses
	numAnchors := 8400
	data := make([]float32, numChannels*numAnchors)
	anchor := 100
	data[0*numAnchors+anchor] = 320 // cx
	data[1*numAnchors+anchor] = 320 // cy
	data[2*numAnchors+anchor] = 200 // w
	data[3*numAnchors+anchor] = 200 // h
	data[4*numAnchors+anchor] = 8   // class 0 logit

	rawOutput := [][]float32{data}
	dims := []int64{1, int64(numChannels), int64(numAnchors)}

	dets, err := pp.Postprocess(rawOutput, dims, 640, 0.5)
	if err != nil {
		t.Fatalf("Postprocess returned error: %v", err)
	}
	if len(dets) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(dets))
	}
	if dets[0].ClassID != 0 {
		t.Errorf("expected class 0, got %d", dets[0].ClassID)
	}
}

func TestYOLOPostprocessor_EmptyOutput(t *testing.T) {
	pp := NewYOLOPostprocessor(0.5, 0.45)
	_, err := pp.Postprocess([][]float32{}, []int64{1, 84, 8400}, 640, 0.5)
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}
