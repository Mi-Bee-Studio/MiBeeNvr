package api

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/doorbell"
)

// fakeDoorbell is a canned DoorbellService.
type fakeDoorbell struct {
	calls    []*doorbell.Call
	answered map[string]error
	rejectd  map[string]error
	byed     map[string]error
}

func (f *fakeDoorbell) Calls() []*doorbell.Call { return f.calls }
func (f *fakeDoorbell) Answer(id string) error {
	if err, ok := f.answered[id]; ok {
		return err
	}
	return doorbell.ErrUnknownCall
}

func (f *fakeDoorbell) Reject(id string, decline bool) error {
	if err, ok := f.rejectd[id]; ok {
		return err
	}
	return doorbell.ErrUnknownCall
}

func (f *fakeDoorbell) Hangup(id string) error {
	if err, ok := f.byed[id]; ok {
		return err
	}
	return doorbell.ErrUnknownCall
}

func (f *fakeDoorbell) TalkBridge(id string) (*doorbell.TalkBridge, error) {
	return nil, doorbell.ErrCallState
}

func (f *fakeDoorbell) SnapshotPath(call *doorbell.Call) (string, bool) {
	return "", false
}

func setupDoorbellAPI(t *testing.T) (*Handler, *fakeDoorbell) {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	f := &fakeDoorbell{answered: map[string]error{}, rejectd: map[string]error{}, byed: map[string]error{}}
	h.SetDoorbellService(f)
	return h, f
}

func doorbellCall(id, state string) *doorbell.Call {
	return &doorbell.Call{ID: id, User: "door1", Name: "Front door", State: state, StartedAt: time.Now()}
}

func TestDoorbellCalls_List(t *testing.T) {
	h, f := setupDoorbellAPI(t)
	f.calls = []*doorbell.Call{doorbellCall("c1", doorbell.CallRinging)}

	rr := doRequest(t, h.Routes(), "GET", "/api/doorbell/calls", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Front door")
	require.Contains(t, rr.Body.String(), "\"state\":\"ringing\"")
}

func TestDoorbellAnswer_NotFoundAndConflict(t *testing.T) {
	h, f := setupDoorbellAPI(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/doorbell/calls/missing/answer", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	f.answered["c1"] = doorbell.ErrCallState
	rr = doRequest(t, h.Routes(), "POST", "/api/doorbell/calls/c1/answer", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	f.answered["c1"] = nil
	rr = doRequest(t, h.Routes(), "POST", "/api/doorbell/calls/c1/answer", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestDoorbellReject_AndBye(t *testing.T) {
	h, f := setupDoorbellAPI(t)

	f.rejectd["c1"] = nil
	rr := doRequest(t, h.Routes(), "POST", "/api/doorbell/calls/c1/reject", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	f.byed["c2"] = doorbell.ErrUnknownCall
	rr = doRequest(t, h.Routes(), "POST", "/api/doorbell/calls/c2/bye", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDoorbellSnapshot_NoSnapshot(t *testing.T) {
	h, f := setupDoorbellAPI(t)
	f.calls = []*doorbell.Call{doorbellCall("c1", doorbell.CallRinging)}

	rr := doRequest(t, h.Routes(), "GET", "/api/doorbell/calls/c1/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, h.Routes(), "GET", "/api/doorbell/calls/zzz/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDoorbellDisabled_Returns501(t *testing.T) {
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/api/doorbell/calls", bytes.NewReader(nil), "", "")
	require.Equal(t, http.StatusNotImplemented, rr.Code)
}
