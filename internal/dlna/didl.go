package dlna

import (
	"fmt"
	"strings"
)

// Object IDs are opaque strings with distinct prefixes so BrowseMetadata can
// route without extra bookkeeping: "0" root, "A<cameraID>" camera container,
// "A<cameraID>D<yyyymmdd>" day container, "R<recordingID>" item.
const (
	objRootPrefix  = "0"
	objCameraID    = "A"
	objDayMarker   = "D"
	objRecordingID = "R"
)

// didlContainer renders a container entry for the DIDL-Lite Result.
func didlContainer(id, parentID, title, childCount string, date string) string {
	return fmt.Sprintf(
		`<container id="%s" parentID="%s" restricted="1" childCount="%s">`+
			`<dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">%s</dc:title>`+
			`<upnp:class xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">object.container</upnp:class>`+
			`%s</container>`,
		xmlEscape(id), xmlEscape(parentID), childCount, xmlEscape(title), didlDate(date))
}

// didlItem renders a playable item with its res resource descriptor. The res
// URL is absolute against the interface base URL the control point used; the
// caller passes the fully resolved URL.
func didlItem(id, parentID, title, date string, rec didlResource) string {
	return fmt.Sprintf(
		`<item id="%s" parentID="%s" restricted="1">`+
			`<dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">%s</dc:title>`+
			`<upnp:class xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">%s</upnp:class>`+
			`%s`+
			`<res protocolInfo="%s" size="%d"%s>%s</res>`+
			`</item>`,
		xmlEscape(id), xmlEscape(parentID), xmlEscape(title), rec.itemClass,
		didlDate(date), xmlEscape(rec.protocolInfo), rec.size, resDurationAttr(rec.durationSec), xmlEscape(rec.url))
}

// resDurationAttr emits the CDS duration attribute when the length is known.
func resDurationAttr(sec float64) string {
	if sec <= 0 {
		return ""
	}
	return ` duration="` + fmtDuration(sec) + `"`
}

func didlDate(date string) string {
	if date == "" {
		return ""
	}
	return `<dc:date xmlns:dc="http://purl.org/dc/elements/1.1/">` + date + `</dc:date>`
}

// didlDocument wraps fragments into a DIDL-Lite document string.
func didlDocument(fragments []string) string {
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	for _, f := range fragments {
		b.WriteString(f)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}

// didlResource carries the res descriptor fields for a recording item.
type didlResource struct {
	protocolInfo string
	itemClass    string
	url          string
	size         int64
	durationSec  float64
}

// mimeForFormat maps an NVR recording format onto the DLNA 4th field of the
// res protocolInfo (mime) and the item's upnp class. MP4 for h264/h265
// (both mux MP4), AVI for the MJPEG recorder, audio MP4 for standalone
// audio sources; timelapse products are MP4.
func mimeForFormat(format string) (mime, itemClass string) {
	switch format {
	case "audio":
		return "audio/mp4", "object.item.audioItem.musicTrack"
	case "mjpeg":
		return "video/avi", "object.item.videoItem"
	default: // h264, h265, timelapse (MP4 family)
		return "video/mp4", "object.item.videoItem"
	}
}
