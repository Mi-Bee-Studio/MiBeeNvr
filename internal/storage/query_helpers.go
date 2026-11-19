package storage

// SQL fragment builders for recording queries: the WHERE/ORDER BY clause
// composition and the cache-key derivation. Split out of db_recording.go —
// these are pure functions with no DB handle.

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// recordingsFilterWhere builds the WHERE clause fragments and bind args shared by
// ListRecordings, ListRecordingsWithTotal, and CountRecordingsWithFilter.
func recordingsFilterWhere(filter model.RecordingFilter) ([]string, []any) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	// Tier filter (#637): default (nil) hides the continuous sub-stream tier
	// from lists/timeline; an explicit layer selects exactly that tier.
	if filter.Layer != nil {
		where = append(where, "COALESCE(layer,0)=?")
		args = append(args, *filter.Layer)
	} else {
		where = append(where, "COALESCE(layer,0)=0")
	}
	if filter.Merged != nil {
		if *filter.Merged {
			where = append(where, "merge_status IN ('merged','daily_merged')")
		} else {
			where = append(where, "merge_status NOT IN ('merged','daily_merged')")
		}
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?")
		args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?")
		args = append(args, formatTime(filter.EndTime))
	}
	if len(filter.Formats) > 0 {
		placeholders := make([]string, len(filter.Formats))
		for i, f := range filter.Formats {
			placeholders[i] = "?"
			args = append(args, string(f))
		}
		where = append(where, "format IN ("+strings.Join(placeholders, ",")+")")
	} else if filter.Format != "" {
		where = append(where, "format=?")
		args = append(args, string(filter.Format))
	}
	if filter.Search != "" {
		pattern := "%" + escapeLike(filter.Search) + "%"
		where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}
	if filter.Archived == nil {
		where = append(where, "archived=0")
	} else if *filter.Archived {
		where = append(where, "archived=1")
	} else {
		where = append(where, "archived=0")
	}
	// AI class filter: keep only recordings that have ≥1 AI event with the given
	// class_name. Uses a correlated EXISTS against ai_events joined on recording_id
	// (both TEXT snowflake IDs). Leverages idx_ai_events_class_recording.
	if filter.AiClass != "" {
		where = append(where, "EXISTS(SELECT 1 FROM ai_events WHERE ai_events.recording_id = recordings.id AND ai_events.class_name = ?)")
		args = append(args, filter.AiClass)
	}
	// Motion filters (issue #435). Unanalyzed recordings (motion_score < 0)
	// never pass a MinMotionScore filter; the activity LIKE matches the
	// comma-separated flags vocabulary (static/motion/scene_cut).
	if filter.MinMotionScore != nil {
		where = append(where, "motion_score >= ?")
		args = append(args, *filter.MinMotionScore)
	}
	if filter.Activity != "" {
		// ESCAPE '\' pairs with escapeLike — flag values contain underscores
		// (scene_cut) that must match literally, not as the single-char wildcard.
		where = append(where, `activity_flags LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(filter.Activity)+"%")
	}
	return where, args
}

// recordingsOrderByClause returns the ORDER BY clause for a filter, restricted to a
// whitelist of sortable columns. Defaults to started_at DESC.
func recordingsOrderByClause(filter model.RecordingFilter) string {
	allowedSortFields := map[string]bool{"started_at": true, "duration": true, "file_size": true, "camera_id": true}
	sortBy := "started_at"
	if filter.SortBy != "" && allowedSortFields[filter.SortBy] {
		sortBy = filter.SortBy
	}
	sortOrder := "DESC"
	if strings.EqualFold(filter.SortOrder, "asc") {
		sortOrder = "ASC"
	}
	return " ORDER BY " + sortBy + " " + sortOrder
}

// scanRecordingRows scans a standard recordings SELECT (no OVER() column) into a slice.
func scanRecordingRows(rows *sql.Rows) ([]model.Recording, error) {
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr, mergePathStr, mergeTierStr, mergeErrorStr, mergeQualityStr sql.NullString
		var mergeProgress sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &mergeQualityStr, &r.Archived, &r.MotionScore, &r.MotionConfidence, &r.ActivityFlags, &r.Layer); err != nil {
			return nil, fmt.Errorf("scan recordings: %w", err)
		}
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			r.MergeStatus = mergeStatusStr.String
		} else if r.MergeStatus == "" {
			r.MergeStatus = model.MergeStatusPending
		}
		if mergePathStr.Valid {
			r.MergePath = mergePathStr.String
		}
		if mergeTierStr.Valid {
			r.MergeTier = mergeTierStr.String
		}
		if mergeProgress.Valid {
			r.MergeProgress = int(mergeProgress.Int64)
		}
		if mergeErrorStr.Valid {
			r.MergeError = mergeErrorStr.String
		}
		if mergeQualityStr.Valid && mergeQualityStr.String != "" {
			r.MergeQuality = mergeQualityStr.String
		} else {
			r.MergeQuality = model.MergeQualityComplete
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, rows.Err()
}
