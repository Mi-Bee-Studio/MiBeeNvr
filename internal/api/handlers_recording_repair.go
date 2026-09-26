package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// handleRepairRecording diagnoses and repairs the KNOWN failure classes of a
// single recording, on demand from the playback-failure overlay. Every fix is
// idempotent and content-safe (in-place MP4 header patches, DB row updates,
// dead-row sweeps) — the same operations the startup sweeps and the
// `repair append-tkhd` CLI run in bulk; this endpoint runs them for the one
// recording the user is looking at. The response reports every action taken
// so the UI can explain the outcome and retry playback when it may help.
//
//	POST /api/recordings/{id}/repair
//
// Failure classes covered (all discovered in the field, all format-generic):
//   - stale ID (the row was consumed by a fold): resolved via merge lineage /
//     covering-row fallback (#915/#917) — no data problem at all;
//   - dead .tmp row (file renamed/vanished, #912-era residue): swept like the
//     startup orphan sweep, then covering fallback if unambiguous;
//   - zero-duration row: re-probed (pure-Go MP4 box walk) and updated;
//   - stsd/tkhd 0×0 visual dims (#853-era append buckets): patched in place
//     from the SPS the file itself carries (merge.RepairZeroDimensions);
//   - file genuinely missing: reported honestly — data cannot be resurrected.
//
// Not repairable here and intentionally not pretended away: codec unsupported
// by the browser (the transcode-to-H.264 button owns that path) and files
// whose bytes are damaged beyond header metadata.
func (h *Handler) handleRepairRecording(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := h.db.GetRecording(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		// The row is gone — the classic fold-consumed stale ID. Resolve it
		// exactly (lineage first, covering fallback second) and tell the
		// caller where the content lives now.
		if fb := h.fallbackRecording(r, id); fb != nil {
			writeJSON(w, http.StatusOK, repairResponse{
				Status:        "resolved_fallback",
				TargetID:      fb.ID,
				Actions:       []string{"stale_id_resolved"},
				RetryPlayback: true,
			})
			return
		}
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	resp := repairResponse{Status: "no_problem_found", Actions: []string{}}

	if _, statErr := os.Stat(rec.FilePath); statErr != nil {
		// Dead-row classes: a .tmp-pathed row whose file vanished is #912-era
		// residue — sweep it exactly like the startup orphan sweep (archived
		// rows stay out of scope, their file semantics belong to offload).
		if strings.HasSuffix(rec.FilePath, ".tmp") && !rec.Archived {
			if _, derr := h.db.DeleteRecordingsBatch(ctx, []string{rec.ID}); derr == nil {
				resp.Actions = append(resp.Actions, "dead_tmp_row_swept")
				if fb := h.fallbackRecording(r, id); fb != nil {
					resp.Status = "resolved_fallback"
					resp.TargetID = fb.ID
					resp.RetryPlayback = true
					writeJSON(w, http.StatusOK, resp)
					return
				}
			}
		}
		resp.Status = "file_missing"
		resp.Actions = append(resp.Actions, "file_not_on_disk")
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Zero-duration row: re-probe the file's own box metadata.
	if rec.Duration <= 0 {
		if d, perr := mediaprobe.ProbeDuration(rec.FilePath); perr == nil && d >= 0.001 {
			endedAt := rec.StartedAt.Add(time.Duration(d * float64(time.Second)))
			if uerr := h.db.UpdateRecordingDuration(ctx, rec.ID, d, endedAt); uerr == nil {
				resp.Actions = append(resp.Actions, fmt.Sprintf("duration_updated=%.1fs", d))
				resp.Status = "repaired"
			}
		}
	}

	// Visual header dims (MP4 only): 0×0 stsd sample entry / tkhd dims make
	// Chromium-family players reject the whole file ("no supported streams")
	// while other players reconstruct size from SPS — repair writes the real
	// size back from the file's own codec config, in place, idempotently.
	if rec.Format == model.FormatH264 || rec.Format == model.FormatH265 {
		if vt, perr := merge.ProbeVideoTrack(rec.FilePath); perr == nil {
			if vt.Width == 0 || vt.Height == 0 || vt.SampleEntryWidth == 0 || vt.SampleEntryHeight == 0 {
				tk, se, rw, rh, rerr := merge.RepairZeroDimensions(rec.FilePath)
				switch {
				case rerr == nil && (tk || se):
					resp.Actions = append(resp.Actions, fmt.Sprintf("stsd_tkhd_dims=%dx%d", rw, rh))
					resp.Status = "repaired"
				case rerr != nil:
					resp.Actions = append(resp.Actions, "dims_repair_failed: "+rerr.Error())
					resp.Status = "unrepairable"
				}
			}
		}
		// A probe error means the file is not a parseable MP4 (or the bytes
		// are damaged beyond metadata) — nothing header-level to fix here.
	}

	resp.RetryPlayback = resp.Status == "repaired" || resp.Status == "no_problem_found"
	writeJSON(w, http.StatusOK, resp)
}

type repairResponse struct {
	// repaired        — at least one problem was found and fixed
	// no_problem_found— data looks healthy; retry playback (error was likely
	//                   transient network/codec — the transcode button owns
	//                   the h265-in-browser case)
	// resolved_fallback — the row is gone/stale; target_id now holds the
	//                   content (navigate there)
	// file_missing    — the media file does not exist; data is gone
	// unrepairable   — a problem was found but the automatic fix failed
	Status        string   `json:"status"`
	TargetID      string   `json:"target_id,omitempty"`
	Actions       []string `json:"actions"`
	RetryPlayback bool     `json:"retry_playback"`
}
