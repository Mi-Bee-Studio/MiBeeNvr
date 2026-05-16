## 2026-05-03 Session Start
- Plan: production-fix (19 tasks, 4 waves)
- Session: ses_2155dda21ffeR73AkeoevhuZtZ
- Starting from: T1 (SSH environment check)

## Auth Bypass Fix (2026-05-03)

### Changes Made
1. **`internal/middleware/auth.go`**:
   - Empty `passwordHash` now returns 401 (was bypassing auth entirely)
   - `CheckPassword()` returns false for empty hash (was returning true — second bypass vector)
   - Removed unused imports (`log`, `sync`, `time`) that appeared after edits

2. **`cmd/mibee-nvr/main.go`**:
   - Added `hash-password` subcommand: `mibee-nvr hash-password <password>` outputs bcrypt hash
   - Added `"strings"` import
   - Added startup WARNING log when `password_hash` is empty

3. **`internal/middleware/auth_test.go`**:
   - Renamed `TestEmptyHashBypass` → `TestEmptyHashRejects` (expects 401)
   - Added `TestCheckPasswordEmptyHash` (verifies false for empty hash)

### Pre-existing Issues Found
- `internal/recorder/h264.go` and `mjpeg.go` have syntax errors (lines 137, 252, 99, 105) — NOT caused by this change
- These prevent `go build ./cmd/mibee-nvr/` from succeeding
- Middleware package builds clean independently

### Test Results
- All 8 middleware tests PASS
- `go vet ./internal/middleware/...` clean
- `go vet ./cmd/mibee-nvr/` fails only due to pre-existing recorder errors

### Key Pattern
- Auth middleware: empty config = reject all, never bypass
- Subcommand parsing: check `os.Args[1]` before `flag.Parse()` or after — current pattern checks after flag.Parse()
