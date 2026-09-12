package recorder

// segmentPreallocEstimate converts the previous segment's final size into
// the fallocate hint for the next one (#757). Recorders know no encoder
// bitrate; the last segment is the best proxy (same camera, same duration
// policy, same scene complexity).
//
//   - 10% headroom absorbs bitrate drift without re-growing the file;
//   - the floor skips pointless preallocation for tiny/failed segments;
//   - the cap stops one pathological segment (e.g. a 4GiB stco-limit run)
//     from reserving absurd space for its successors.
func segmentPreallocEstimate(lastSegBytes int64) int64 {
	const floor = 4 << 20      // 4MiB — below this, extent churn is negligible
	const capBytes = 512 << 20 // 512MiB — stco-limit headroom is 4GiB
	if lastSegBytes < floor {
		return 0
	}
	est := lastSegBytes * 11 / 10
	if est > capBytes {
		return capBytes
	}
	return est
}
