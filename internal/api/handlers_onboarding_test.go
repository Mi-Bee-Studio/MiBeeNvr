package api

// Zero-touch onboarding API tests (#1120): the routes answer 409 when the
// feature is disabled (the default), and the status/job payloads shape up
// when a service is wired.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onboarding"
	"github.com/stretchr/testify/require"
)

type fakeOnboarding struct {
	status     onboarding.Status
	scanJobID  string
	adoptJobID string
	scanErr    error
	job        onboarding.Job
}

func (f *fakeOnboarding) Status() onboarding.Status { return f.status }

func (f *fakeOnboarding) StartScanJob() (string, error) {
	return f.scanJobID, f.scanErr
}

func (f *fakeOnboarding) StartAdoptJob(req onboarding.AdoptRequest) (string, error) {
	return f.adoptJobID, f.scanErr
}

func (f *fakeOnboarding) Job(id string) (onboarding.Job, bool) { return f.job, id == f.job.ID }

func TestOnboarding_DisabledByDefault_409(t *testing.T) {
	// No service wired (the default config leaves camera_onboarding off) —
	// every endpoint must answer 409, never 404/500, so the UI can probe
	// capability and tell the operator to enable it.
	h := TestHandler(nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/onboarding/status", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)
	require.Contains(t, rr.Body.String(), "camera_onboarding.enabled")

	rr = doRequest(t, h.Routes(), "POST", "/api/onboarding/scan", strings.NewReader("{}"), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	rr = doRequest(t, h.Routes(), "POST", "/api/onboarding/adopt", strings.NewReader(`{"ip":"192.168.1.64"}`), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	rr = doRequest(t, h.Routes(), "GET", "/api/onboarding/jobs/abc", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestOnboarding_Status_Shaped(t *testing.T) {
	h := TestHandler(nil, nil)
	h.SetOnboardingService(&fakeOnboarding{status: onboarding.Status{
		Enabled:     true,
		Subnet:      "192.168.1.0/24",
		AliasIP:     "192.168.1.254",
		AliasActive: false,
	}})

	rr := doRequest(t, h.Routes(), "GET", "/api/onboarding/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.Contains(t, rr.Body.String(), `"subnet":"192.168.1.0/24"`)
}

func TestOnboarding_ScanAccepted(t *testing.T) {
	h := TestHandler(nil, nil)
	h.SetOnboardingService(&fakeOnboarding{scanJobID: "job-1"})

	rr := doRequest(t, h.Routes(), "POST", "/api/onboarding/scan", strings.NewReader("{}"), "", "")
	require.Equal(t, http.StatusAccepted, rr.Code)
	require.Contains(t, rr.Body.String(), `"job_id":"job-1"`)
}

func TestOnboarding_AdoptValidatesBody(t *testing.T) {
	h := TestHandler(nil, nil)
	h.SetOnboardingService(&fakeOnboarding{adoptJobID: "job-2"})

	// Missing ip → 400.
	rr := doRequest(t, h.Routes(), "POST", "/api/onboarding/adopt", strings.NewReader(`{"username":"admin"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, h.Routes(), "POST", "/api/onboarding/adopt", strings.NewReader(`{"ip":"192.168.1.64"}`), "", "")
	require.Equal(t, http.StatusAccepted, rr.Code)
	require.Contains(t, rr.Body.String(), `"job_id":"job-2"`)
}

func TestOnboarding_JobSnapshot(t *testing.T) {
	h := TestHandler(nil, nil)
	h.SetOnboardingService(&fakeOnboarding{
		job: onboarding.Job{
			ID: "job-9", Kind: "adopt", State: onboarding.JobDone, StartedAt: time.Now(),
			Adopt: &onboarding.AdoptResult{NewIP: "192.168.63.77", CameraID: "cam-1"},
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/onboarding/jobs/job-9", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"done"`)
	require.Contains(t, rr.Body.String(), `"new_ip":"192.168.63.77"`)

	rr = doRequest(t, h.Routes(), "GET", "/api/onboarding/jobs/missing", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
