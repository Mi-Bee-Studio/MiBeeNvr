package thermal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// KindHikvision selects the Hikvision ISAPI radiometric readout: instead of
// a SPEC-style JSON current endpoint, the NVR pulls the device's
// pixel-to-pixel thermometry frame (GET /ISAPI/Thermal/channels/<ch>/
// thermometry/jpegPicWithAppendData?format=json, HTTP Digest) — a multipart
// body carrying a JSON descriptor, the thermal JPEG, the full per-pixel
// temperature matrix (float32 °C, row-major) and, when the device's
// visiblePicEnabled is on, the visible-light JPEG as well. Field reference:
// DS-2TB2617 (firmware V5.5.38) — 160×120 matrix, ~77 KB per frame without
// the visible picture (~900 KB with it: switch visiblePicEnabled off).
const KindHikvision = "hikvision"

// maxRadiometricBytes caps one multipart body (a 640×512 matrix of float32
// is 1.3 MB; the optional visible JPEG is never read past its header).
const maxRadiometricBytes = 4 << 20

// NormRect is a normalized (0..1) rectangle.
type NormRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// RadiometricFrame is one decoded per-pixel temperature frame.
type RadiometricFrame struct {
	Cols, Rows int
	Values     []float32 // °C, row-major, len == Cols*Rows
	// Frozen marks a frame the device flagged as frozen (shutter / NUC
	// calibration) — not a live measurement.
	Frozen bool
	// VisibleRect is where the thermal FOV sits inside the visible picture
	// (the device's dual-spectrum registration), when reported.
	VisibleRect *NormRect
}

// hikJPEGWithAppendData mirrors the JSON descriptor part.
type hikJPEGWithAppendData struct {
	JpegPictureWithAppendData struct {
		Channel               int  `json:"channel"`
		JpegPicWidth          int  `json:"jpegPicWidth"`
		JpegPicHeight         int  `json:"jpegPicHeight"`
		P2PDataLen            int  `json:"p2pDataLen"`
		IsFreezedata          bool `json:"isFreezedata"`
		TemperatureDataLength int  `json:"temperatureDataLength"`
		VisibleValidRect      *struct {
			Height float64 `json:"height"`
			Width  float64 `json:"width"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
		} `json:"VisibleValidRect"`
	} `json:"JpegPictureWithAppendData"`
}

// HikvisionFrameURL builds the radiometric-frame URL from a device base
// ("http://host[:port]", any path ignored) and the thermal channel number.
func HikvisionFrameURL(base string, channel int) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("hikvision readout: base must be http(s)://host[:port], got %q", base)
	}
	if channel <= 0 {
		channel = 2
	}
	return fmt.Sprintf("%s://%s/ISAPI/Thermal/channels/%d/thermometry/jpegPicWithAppendData?format=json",
		u.Scheme, u.Host, channel), nil
}

// FetchHikvisionFrame pulls and decodes one radiometric frame. client must
// carry the device credentials (a DigestTransport — ISAPI refuses Basic by
// default).
func FetchHikvisionFrame(ctx context.Context, client *http.Client, frameURL string) (*RadiometricFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, frameURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hikvision readout: HTTP %d", resp.StatusCode)
	}
	return ParseHikvisionFrame(resp.Header.Get("Content-Type"), io.LimitReader(resp.Body, maxRadiometricBytes))
}

// ParseHikvisionFrame decodes the multipart body. It stops reading after
// the temperature matrix — the optional visible JPEG behind it is never
// pulled through.
func ParseHikvisionFrame(contentType string, body io.Reader) (*RadiometricFrame, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, fmt.Errorf("hikvision readout: not a multipart reply (%q)", contentType)
	}
	mr := multipart.NewReader(body, params["boundary"])
	var desc *hikJPEGWithAppendData
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hikvision readout: multipart: %w", err)
		}
		ct := strings.ToLower(part.Header.Get("Content-Type"))
		switch {
		case strings.Contains(ct, "json"):
			var d hikJPEGWithAppendData
			if err := json.NewDecoder(part).Decode(&d); err != nil {
				return nil, fmt.Errorf("hikvision readout: descriptor: %w", err)
			}
			desc = &d
		case strings.Contains(ct, "octet-stream"):
			if desc == nil {
				return nil, errors.New("hikvision readout: temperature matrix before its descriptor")
			}
			return decodeHikMatrix(desc, part)
		}
	}
	return nil, errors.New("hikvision readout: no temperature matrix in reply (is pixel-to-pixel thermometry enabled?)")
}

func decodeHikMatrix(desc *hikJPEGWithAppendData, r io.Reader) (*RadiometricFrame, error) {
	d := desc.JpegPictureWithAppendData
	w, h := d.JpegPicWidth, d.JpegPicHeight
	if w <= 0 || h <= 0 || w*h > 1<<20 {
		return nil, fmt.Errorf("hikvision readout: bad matrix geometry %dx%d", w, h)
	}
	if d.TemperatureDataLength != 4 {
		// The 2-byte encoding is not self-describing in the descriptor;
		// only the float32 form is decoded.
		return nil, fmt.Errorf("hikvision readout: temperatureDataLength %d unsupported (set 4 in pixelToPixelParam)", d.TemperatureDataLength)
	}
	buf := make([]byte, w*h*4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("hikvision readout: matrix: %w", err)
	}
	f := &RadiometricFrame{Cols: w, Rows: h, Values: make([]float32, w*h), Frozen: d.IsFreezedata}
	for i := range f.Values {
		f.Values[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	if v := d.VisibleValidRect; v != nil && v.Width > 0 && v.Height > 0 {
		f.VisibleRect = &NormRect{X: v.X, Y: v.Y, W: v.Width, H: v.Height}
	}
	return f, nil
}

// extreme is one scene extreme with its cell.
type extreme struct {
	v    float64
	x, y int
}

// extremes returns the hottest and coldest plausible cells (NaN/Inf and
// sensor sentinels outside -100..1000 °C are skipped).
func (f *RadiometricFrame) extremes() (maxE, minE extreme, ok bool) {
	for i, raw := range f.Values {
		v := float64(raw)
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -100 || v > 1000 {
			continue
		}
		e := extreme{v: v, x: i % f.Cols, y: i / f.Cols}
		if !ok {
			maxE, minE, ok = e, e, true
			continue
		}
		if v > maxE.v {
			maxE = e
		}
		if v < minE.v {
			minE = e
		}
	}
	return maxE, minE, ok
}

// Reading converts the frame to a chip reading: scene max/min plus the
// centre-point meter Hikvision's own UI shows. ok=false for an empty or
// all-invalid matrix.
func (f *RadiometricFrame) Reading(cameraID string) (Reading, bool) {
	maxE, minE, ok := f.extremes()
	if !ok {
		return Reading{}, false
	}
	r := Reading{CameraID: cameraID, MaxC: round1p(maxE.v), MinC: round1p(minE.v)}
	cx, cy := f.Cols/2, f.Rows/2
	if c := float64(f.Values[cy*f.Cols+cx]); !math.IsNaN(c) && c > -100 && c < 1000 {
		r.Spots = []SpotReading{{Name: "center", X: 0.5, Y: 0.5, TempC: *round1p(c)}}
	}
	return r, true
}

// FramePayload renders the frame in the shape the live view's spot-probe
// layer reads (the SPEC grid object, extended with its geometry):
// {"grid":{"cols","rows","values"},"max":{"value_c","x","y"},"min":{…},
// "frozen", "visible_rect"}. Values are rounded to 0.1 °C (the sensor's
// own resolution) to keep the 160×120 matrix near 100 KB of JSON.
func (f *RadiometricFrame) FramePayload() map[string]any {
	vals := make([]json.Number, len(f.Values))
	for i, v := range f.Values {
		fv := float64(v)
		if math.IsNaN(fv) || math.IsInf(fv, 0) {
			fv = 0
		}
		vals[i] = json.Number(strconv.FormatFloat(math.Round(fv*10)/10, 'f', -1, 64))
	}
	out := map[string]any{
		"grid":   map[string]any{"cols": f.Cols, "rows": f.Rows, "values": vals},
		"frozen": f.Frozen,
	}
	if maxE, minE, ok := f.extremes(); ok {
		out["max"] = map[string]any{"value_c": *round1p(maxE.v), "x": maxE.x, "y": maxE.y}
		out["min"] = map[string]any{"value_c": *round1p(minE.v), "x": minE.x, "y": minE.y}
	}
	if f.VisibleRect != nil {
		out["visible_rect"] = f.VisibleRect
	}
	return out
}

func round1p(v float64) *float64 {
	r := math.Round(v*10) / 10
	return &r
}
