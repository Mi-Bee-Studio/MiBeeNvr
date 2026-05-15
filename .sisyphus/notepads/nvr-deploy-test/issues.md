# Issues - nvr-deploy-test

## 2026-04-30 Session 2

### Bug #7: FTP Download Broken (FIXED)
- **File**: `internal/ftp/server.go` line 286
- **Problem**: `os.O_RDONLY` is 0 in Go, so `flags&os.O_RDONLY != 0` is always false
- **Impact**: ALL FTP downloads routed to upload handler instead of download handler
- **Fix**: Changed to `flags == os.O_RDONLY`
- **Root cause**: Bitwise AND with zero is always zero

### Bug #8: SQLite Timestamp Format Incompatible (FIXED)
- **File**: `internal/storage/db.go`
- **Problem**: Go's `time.Time.String()` produces format like `2026-04-30 22:52:10.109803985 +0800 CST m=+32.026969936`
- **Impact**: SQLite `datetime('now')` comparison fails — cleanup never triggers
- **Fix**: Added `timeToDB()`/`parseTime()` helpers using UTC format `2006-01-02 15:04:05.999999999`
- **Backward compat**: `parseTime()` handles legacy Go format for existing data

### OOM on RPi 3B (WORKAROUND)
- **Problem**: MP4Muxer stores all samples in memory until segment close
- **Impact**: 2-minute segments use 60MB+ RAM, causing OOM on 905MB device
- **Fix**: Changed `segment_duration` from 2m to 30s (reduces per-segment memory to ~15-20MB)
- **Long-term**: Need streaming MP4 writer for longer segments

### retention_days: 0 Treated as Default
- **File**: `internal/config/config.go` line 132-133
- **Problem**: `retention_days: 0` gets overridden to default (30) by `applyDefaults()`
- **Impact**: Cannot use 0 to mean "delete everything"
- **Status**: Known behavior, documented in README
