package dlna

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// contentDirectory adapts the recording store into the CDS browse tree:
// root → camera containers → day containers → recording items.
type contentDirectory struct {
	db    *storage.DB
	store *storage.Manager
}

// browseResult is the CDS Browse response payload: the container/item
// fragments plus the total match count. The SOAP layer applies CDS
// pagination before assembling the DIDL-Lite document.
type browseResult struct {
	frags        []string
	totalMatches int
}

// browse answers BrowseDirectChildren / BrowseMetadata for an object ID.
func (c *contentDirectory) browse(ctx context.Context, objectID, browseFlag, baseURL string) (browseResult, error) {
	switch browseFlag {
	case "BrowseDirectChildren":
		return c.browseChildren(ctx, objectID, baseURL)
	case "BrowseMetadata":
		return c.browseMetadata(ctx, objectID, baseURL)
	default:
		return browseResult{}, fmt.Errorf("unsupported BrowseFlag %q", browseFlag)
	}
}

func (c *contentDirectory) browseChildren(ctx context.Context, objectID, baseURL string) (browseResult, error) {
	switch {
	case objectID == objRootPrefix:
		return c.rootChildren(ctx)
	case isDayObject(objectID):
		return c.dayChildren(ctx, objectID, baseURL)
	case isCameraObject(objectID):
		return c.cameraChildren(ctx, cameraIDOf(objectID))
	default:
		return browseResult{}, errNoSuchObject
	}
}

func (c *contentDirectory) browseMetadata(ctx context.Context, objectID, baseURL string) (browseResult, error) {
	switch {
	case objectID == objRootPrefix:
		cams, err := c.db.ListCameras(ctx)
		if err != nil {
			return browseResult{}, err
		}
		return singleResult(didlContainer(objRootPrefix, "-1", "MiBee NVR", strconv.Itoa(len(cams)), "")), nil
	case isCameraObject(objectID):
		cam, err := c.cameraByID(ctx, cameraIDOf(objectID))
		if err != nil {
			return browseResult{}, err
		}
		days, err := c.cameraDays(ctx, cam.ID)
		if err != nil {
			return browseResult{}, err
		}
		return singleResult(didlContainer(cameraObjectID(cam.ID), objRootPrefix, cameraTitle(cam), strconv.Itoa(len(days)), "")), nil
	case isDayObject(objectID):
		cam, err := c.cameraByID(ctx, cameraIDOf(objectID))
		if err != nil {
			return browseResult{}, err
		}
		date := dayOf(objectID)
		recs, err := c.dayRecordings(ctx, cam.ID, date)
		if err != nil {
			return browseResult{}, err
		}
		return singleResult(didlContainer(objectID, cameraObjectID(cam.ID), cameraTitle(cam)+" · "+date, strconv.Itoa(len(recs)), "")), nil
	case isRecordingObject(objectID):
		rec, err := c.db.GetRecording(ctx, recordingIDOf(objectID))
		if err != nil || rec == nil {
			return browseResult{}, errNoSuchObject
		}
		return singleResult(c.recordingItem(*rec, dayObjectID(rec.CameraID, localDate(rec.StartedAt)), baseURL)), nil
	default:
		return browseResult{}, errNoSuchObject
	}
}

// rootChildren lists one container per camera.
func (c *contentDirectory) rootChildren(ctx context.Context) (browseResult, error) {
	cams, err := c.db.ListCameras(ctx)
	if err != nil {
		return browseResult{}, err
	}
	sort.Slice(cams, func(i, j int) bool { return cameraTitle(cams[i]) < cameraTitle(cams[j]) })
	var frags []string
	for _, cam := range cams {
		days, err := c.cameraDays(ctx, cam.ID)
		if err != nil {
			return browseResult{}, err
		}
		frags = append(frags, didlContainer(cameraObjectID(cam.ID), objRootPrefix, cameraTitle(cam), strconv.Itoa(len(days)), ""))
	}
	return result(frags), nil
}

// cameraChildren lists one container per day that has recordings.
func (c *contentDirectory) cameraChildren(ctx context.Context, cameraID string) (browseResult, error) {
	if _, err := c.cameraByID(ctx, cameraID); err != nil {
		return browseResult{}, err
	}
	days, err := c.cameraDays(ctx, cameraID)
	if err != nil {
		return browseResult{}, err
	}
	cam, _ := c.cameraByID(ctx, cameraID)
	var frags []string
	for _, day := range days {
		recs, err := c.dayRecordings(ctx, cameraID, day)
		if err != nil {
			return browseResult{}, err
		}
		frags = append(frags, didlContainer(dayObjectID(cameraID, day), cameraObjectID(cameraID), cameraTitle(cam)+" · "+day, strconv.Itoa(len(recs)), ""))
	}
	return result(frags), nil
}

// dayChildren lists the day's recordings as items.
func (c *contentDirectory) dayChildren(ctx context.Context, objectID, baseURL string) (browseResult, error) {
	cam, err := c.cameraByID(ctx, cameraIDOf(objectID))
	if err != nil {
		return browseResult{}, err
	}
	recs, err := c.dayRecordings(ctx, cam.ID, dayOf(objectID))
	if err != nil {
		return browseResult{}, err
	}
	parent := dayObjectID(cam.ID, dayOf(objectID))
	var frags []string
	for _, rec := range recs {
		frags = append(frags, c.recordingItem(rec, parent, baseURL))
	}
	return result(frags), nil
}

// recordingItem maps one recording row to a DIDL item. The res URL is
// relative (/dlna/item/<id>); the SOAP layer renders it against the base URL
// of the interface the request arrived on.
func (c *contentDirectory) recordingItem(rec model.Recording, parentID, baseURL string) string {
	mime, class := mimeForFormat(string(rec.Format))
	title := rec.StartedAt.Local().Format("15:04:05")
	if rec.Duration >= 3600 {
		title = fmt.Sprintf("%s (%s)", title, fmtDuration(rec.Duration))
	}
	return didlItem(recordingObjectID(rec.ID), parentID, title, rec.StartedAt.Local().Format(time.RFC3339), didlResource{
		protocolInfo: "http-get:*:" + mime + ":*",
		itemClass:    class,
		url:          baseURL + itemURLPath(rec.ID),
		size:         rec.FileSize,
		durationSec:  rec.Duration,
	})
}

// cameraDays returns the descending distinct local dates with recordings.
func (c *contentDirectory) cameraDays(ctx context.Context, cameraID string) ([]string, error) {
	recs, err := c.db.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Limit:     10000,
		SortBy:    "started_at",
		SortOrder: "desc",
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var days []string
	for _, r := range recs {
		d := localDate(r.StartedAt)
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	return days, nil
}

// dayRecordings returns the day's recordings oldest-first.
func (c *contentDirectory) dayRecordings(ctx context.Context, cameraID, date string) ([]model.Recording, error) {
	start, err := time.ParseInLocation("20060102", date, time.Local)
	if err != nil {
		return nil, errNoSuchObject
	}
	end := start.Add(24 * time.Hour)
	recs, err := c.db.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: start,
		EndTime:   end,
		Limit:     10000,
		SortBy:    "started_at",
		SortOrder: "asc",
	})
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func (c *contentDirectory) cameraByID(ctx context.Context, id string) (storage.CameraRow, error) {
	cams, err := c.db.ListCameras(ctx)
	if err != nil {
		return storage.CameraRow{}, err
	}
	for _, cam := range cams {
		if cam.ID == id {
			return cam, nil
		}
	}
	return storage.CameraRow{}, errNoSuchObject
}

// recordingPath resolves a recording's absolute file path through the store
// (per-camera storage roots included) with containment enforced.
func (c *contentDirectory) recordingPath(rec *model.Recording) (string, error) {
	base := c.store.RootFor(rec.CameraID)
	return storage.ValidatePath(base, rec.FilePath)
}

// errNoSuchObject maps to the CDS 701 error code.
var errNoSuchObject = fmt.Errorf("no such object")

func cameraObjectID(cameraID string) string { return objCameraID + cameraID }
func dayObjectID(camID, date string) string { return objCameraID + camID + objDayMarker + date }
func recordingObjectID(recID string) string { return objRecordingID + recID }
func itemURLPath(recID string) string       { return "/dlna/item/" + recID }

// cameraIDOf strips the container prefix AND the day marker suffix: for a
// day object ("A<cam>D<date>") the camera ID is the part before the "D".
func cameraIDOf(objectID string) string {
	s := strings.TrimPrefix(objectID, objCameraID)
	if i := strings.Index(s, objDayMarker); i >= 0 {
		s = s[:i]
	}
	return s
}
func recordingIDOf(objectID string) string { return strings.TrimPrefix(objectID, objRecordingID) }

func isCameraObject(objectID string) bool {
	return strings.HasPrefix(objectID, objCameraID) && !strings.Contains(objectID, objDayMarker) && !isRecordingObject(objectID)
}

func isDayObject(objectID string) bool {
	return strings.HasPrefix(objectID, objCameraID) && strings.Contains(objectID, objDayMarker)
}

func isRecordingObject(objectID string) bool {
	return strings.HasPrefix(objectID, objRecordingID)
}

// dayOf extracts the yyyymmdd suffix of a day object ID.
func dayOf(objectID string) string {
	if i := strings.Index(objectID, objDayMarker); i >= 0 {
		return objectID[i+len(objDayMarker):]
	}
	return ""
}

func localDate(t time.Time) string { return t.Local().Format("20060102") }

func cameraTitle(cam storage.CameraRow) string {
	if strings.TrimSpace(cam.Name) != "" {
		return cam.Name
	}
	return cam.ID
}

func result(frags []string) browseResult {
	return browseResult{frags: frags, totalMatches: len(frags)}
}

func singleResult(frag string) browseResult {
	return browseResult{frags: []string{frag}, totalMatches: 1}
}
