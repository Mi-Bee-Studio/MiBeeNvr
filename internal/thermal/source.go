package thermal

import (
	"net/url"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// SourceForCamera resolves a camera's thermal_readout block into a poller
// source. ok=false when the camera is not a thermal channel or carries no
// usable readout. Shared by the boot-wired poller and the API's spot-probe
// frame proxy so both read the device the same way.
//
// For kind "hikvision" the URL is optional: an empty one means the camera's
// own host (ONVIF endpoint, else stream URL), and the camera's credentials
// answer the ISAPI Digest challenge.
func SourceForCamera(cam *config.CameraConfig) (ReadoutSource, bool) {
	if cam == nil || cam.ImagingType != config.ImagingTypeThermal || cam.ThermalReadout == nil {
		return ReadoutSource{}, false
	}
	tr := cam.ThermalReadout
	src := ReadoutSource{
		AuthHeader: tr.AuthHeader,
		AuthValue:  tr.AuthValue,
		Interval:   time.Duration(tr.IntervalS) * time.Second,
		Kind:       tr.Kind,
	}
	if tr.Kind != config.ThermalReadoutKindHikvision {
		if tr.URL == "" {
			return ReadoutSource{}, false
		}
		src.URL = tr.URL
		return src, true
	}
	base := tr.URL
	if base == "" {
		base = hostBase(cam.ONVIFEndpoint)
	}
	if base == "" {
		base = hostBase(cam.URL)
	}
	frameURL, err := HikvisionFrameURL(base, tr.Channel)
	if err != nil {
		return ReadoutSource{}, false
	}
	src.URL = frameURL
	src.Username = cam.Username
	src.Password = cam.Password
	return src, true
}

// hostBase reduces any device URL (ONVIF endpoint, rtsp://…) to the HTTP
// base of the same host: "http://host[:port]" for http(s) URLs, plain
// "http://host" for other schemes (the ISAPI web port is the default one).
func hostBase(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "https":
		return u.Scheme + "://" + u.Host
	default:
		return "http://" + u.Hostname()
	}
}
