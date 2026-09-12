package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCameraGroupRoundtrip covers the v36 grouping column end to end at the
// storage layer: default empty, UpdateCameraGroup round-trips through both
// GetCamera and ListCameras (list/get must agree — the SPA derives the group
// sections from the list), empty string ungroups, and a missing row is a
// silent no-op.
func TestCameraGroupRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test_group.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.UpsertCamera(ctx, "cam-a", "A", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-b", "B", "rtsp", "h264", "", "", "", "", "", "", ""))

	// Default: ungrouped, and GroupName is '' (not NULL) via COALESCE.
	row, err := db.GetCamera(ctx, "cam-a")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "", row.GroupName)

	// Assign groups.
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-a", "室内"))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-b", "室外"))
	row, err = db.GetCamera(ctx, "cam-a")
	require.NoError(t, err)
	require.Equal(t, "室内", row.GroupName)

	cams, err := db.ListCameras(ctx)
	require.NoError(t, err)
	groups := map[string]string{}
	for _, c := range cams {
		groups[c.ID] = c.GroupName
	}
	require.Equal(t, "室内", groups["cam-a"])
	require.Equal(t, "室外", groups["cam-b"])

	// Rename a group (both cameras share it) and ungroup one.
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-a", "一层"))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-b", ""))
	row, err = db.GetCamera(ctx, "cam-b")
	require.NoError(t, err)
	require.Equal(t, "", row.GroupName)
	cams, err = db.ListCameras(ctx)
	require.NoError(t, err)
	for _, c := range cams {
		if c.ID == "cam-a" {
			require.Equal(t, "一层", c.GroupName)
		}
	}

	// Archived cameras keep their group (ListArchivedCameras reads the same
	// column; un-archiving restores the label without re-entry).
	require.NoError(t, db.ArchiveCameraDB(ctx, "cam-a"))
	archived, err := db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	found := false
	for _, c := range archived {
		if c.ID == "cam-a" {
			found = true
			require.Equal(t, "一层", c.GroupName)
		}
	}
	require.True(t, found, "archived cam-a should appear in ListArchivedCameras")

	// Missing row: silent no-op (contract matches UpdateCameraStableID).
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-missing", "X"))
}

// TestEnsureCameraGroupNameColumn_Idempotent re-runs Init on an existing
// database — the pragma-guarded ALTER must be a no-op, not a "duplicate column"
// error (every NVR restart goes through Init).
func TestEnsureCameraGroupNameColumn_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_group_mig.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.UpsertCamera(ctx, "cam-x", "X", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-x", "G"))
	require.NoError(t, db.Close())

	// Reopen — Init runs ensureCameraGroupNameColumn again on the v36 schema.
	db2, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db2.Init(ctx))
	t.Cleanup(func() { _ = db2.Close() })
	row, err := db2.GetCamera(ctx, "cam-x")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "G", row.GroupName)
}

// TestCameraGroupRegistry covers the v37 registry: empty groups persist,
// rename walks the member cameras (including merge-onto-existing), delete
// ungroups members, and derived-only groups (no registry row) still rename/
// delete through the same calls.
func TestCameraGroupRegistry(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test_group_reg.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	// Empty list starts empty (nil → handled by caller, list call itself works).
	groups, err := db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Empty(t, groups)

	// Create two groups; idempotent re-create.
	require.NoError(t, db.UpsertCameraGroup(ctx, "一层"))
	require.NoError(t, db.UpsertCameraGroup(ctx, "二层"))
	require.NoError(t, db.UpsertCameraGroup(ctx, "一层"))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"一层", "二层"}, groups)

	// Empty group survives with no member camera.
	require.NoError(t, db.UpsertCamera(ctx, "cam-r1", "R1", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-r2", "R2", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-r1", "一层"))
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-r2", "二层"))
	require.NoError(t, db.RenameCameraGroup(ctx, "一层", "前院"))
	row, err := db.GetCamera(ctx, "cam-r1")
	require.NoError(t, err)
	require.Equal(t, "前院", row.GroupName)
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"前院", "二层"}, groups)

	// Rename onto an existing name MERGES the member sets.
	require.NoError(t, db.RenameCameraGroup(ctx, "二层", "前院"))
	for _, id := range []string{"cam-r1", "cam-r2"} {
		row, err = db.GetCamera(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "前院", row.GroupName)
	}
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"前院"}, groups)

	// Derived-only group (no registry row): rename still moves cameras and
	// registers the target.
	require.NoError(t, db.UpdateCameraGroup(ctx, "cam-r2", "后院"))
	require.NoError(t, db.RenameCameraGroup(ctx, "后院", "车库"))
	row, err = db.GetCamera(ctx, "cam-r2")
	require.NoError(t, err)
	require.Equal(t, "车库", row.GroupName)
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"前院", "车库"}, groups)

	// Delete ungroups members and drops the row; derived-only delete works.
	ungrouped, err := db.DeleteCameraGroup(ctx, "前院")
	require.NoError(t, err)
	require.Equal(t, int64(1), ungrouped)
	row, err = db.GetCamera(ctx, "cam-r1")
	require.NoError(t, err)
	require.Equal(t, "", row.GroupName)
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"车库"}, groups)

	// Invalid args.
	require.Error(t, db.RenameCameraGroup(ctx, "", "x"))
	require.Error(t, db.RenameCameraGroup(ctx, "x", ""))
	require.Error(t, db.RenameCameraGroup(ctx, "same", "same"))
	_, err = db.DeleteCameraGroup(ctx, "")
	require.Error(t, err)
}

// TestCameraGroupOrdering covers v38: SetCameraGroupsOrder persists a custom
// order, new groups append to the end, and a rename keeps its slot (merging
// keeps the target's slot).
func TestCameraGroupOrdering(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test_group_ord.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.UpsertCameraGroup(ctx, "甲"))
	require.NoError(t, db.UpsertCameraGroup(ctx, "乙"))
	require.NoError(t, db.UpsertCameraGroup(ctx, "丙"))
	groups, err := db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"甲", "乙", "丙"}, groups, "creation appends in order")

	// Reorder: swap 甲/乙, keep 丙, and register a derived group 丁 in slot 1.
	require.NoError(t, db.SetCameraGroupsOrder(ctx, []string{"乙", "丁", "甲", "丙"}))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"乙", "丁", "甲", "丙"}, groups)

	// New group appends after the reordered tail.
	require.NoError(t, db.UpsertCameraGroup(ctx, "戊"))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"乙", "丁", "甲", "丙", "戊"}, groups)

	// Rename keeps the slot.
	require.NoError(t, db.RenameCameraGroup(ctx, "丁", "丁新"))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"乙", "丁新", "甲", "丙", "戊"}, groups)

	// Merging onto an existing group keeps the TARGET's slot.
	require.NoError(t, db.RenameCameraGroup(ctx, "戊", "丙"))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"乙", "丁新", "甲", "丙"}, groups)

	// Re-running the same order is idempotent.
	require.NoError(t, db.SetCameraGroupsOrder(ctx, []string{"乙", "丁新", "甲", "丙"}))
	groups, err = db.ListCameraGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"乙", "丁新", "甲", "丙"}, groups)
}
