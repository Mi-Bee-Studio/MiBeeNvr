## 2026-05-12 Session Start
- Plan: deployment-fix
- Session: ses_1e64fa118ffevuhuRh47yjuHtw
- 15 tasks + 4 Final Verification tasks
- 4 waves of parallel execution

## Plaintext Password Auto-Hash Fix
- `NewAuthMiddleware` now returns `(middleware, effectiveHash)` tuple so callers can persist auto-hashed passwords back to config
- When changing function signatures that return tuples, ALL call sites must handle both return values — AST replace for args alone misses the assignment side
- `config.Save()` uses atomic write (temp+rename), safe to call during startup
- bcrypt hashing on ARM (RPi 3B) takes ~430ms — acceptable as one-time startup cost when auto-hashing
- Pre-existing test failures in webdav (PATCH 405 vs 403) are unrelated to auth changes

## README Update (T13)
- Both README.md and README.zh.md updated with 4 install methods: pre-built binary, Docker, one-click install, build from source
- GitHub Releases link: https://github.com/Mi-Bee-Studio/MiBeeNvr/releases
- `mibee-nvr init --password` is the key quick-start command
- docker-compose.yml references `ghcr.io/mi-bee-studio/mibeen-nvr:latest` (note: possible typo "mibeen" vs "mibee" in image name)
- EN/ZH section headers must match 1:1 for parity
- Docker Container Images section updated with docker-compose quick reference in both languages

## Integration Tests for CLI and Auth (T8)

### Test Files Created/Modified
- `internal/middleware/auth_test.go`: Added `TestPlaintextPasswordAutoHash` and `TestHashTakesPriorityOverPlaintext`
- `tests/cli_test.go`: New file with 6 test cases for `init` and `health` subcommands

### Key Learnings
- `go test` runs from the package directory, so `go build ./cmd/mibee-nvr` from `tests/` needs `../cmd/mibee-nvr`
- Health subcommand constructs URL as `"http://localhost" + addr` — addr MUST start with `:` (e.g., `:9090`)
- `httptest.NewServer` gives `127.0.0.1:PORT` — need `net.SplitHostPort` to extract port and format as `:PORT`
- Existing auth_test.go uses plain `testing` (not `testify/require`) — added new tests alongside using `require`
- `buildBinary` helper compiles the binary once per test, caching in temp dir via `t.Helper()`
- All 8 new tests (2 auth + 6 CLI) pass. Pre-existing webdav PATCH failure (403 vs 405) is unrelated.

## Final Verification Wave
- F1 REJECTED: install.sh URL format mismatch (mibee-nvr-v0.2.0-arm64 vs mibee-nvr-arm64) — FIXED by removing version from filename
- F1 also found: docker-compose.yml image name typo (mibeen-nvr → mibee-nvr) — FIXED
- F2 APPROVED: 6 cosmetic style issues (indentation, trailing whitespace, duplicate comment) — acceptable, no functional issues
- F3 APPROVED: 21/21 QA scenarios pass, 11/11 integration, 8 edge cases tested
- F4 APPROVED: 14/15 tasks compliant, 0 cross-task contamination, 0 unaccounted files

## Post-Fix Verification
- install.sh URL now generates mibee-nvr-${arch} (matches release.yml output)
- docker-compose image now ghcr.io/mi-bee-studio/mibee-nvr:latest
- Full test suite: 552 pass / 2 fail (pre-existing webdav PATCH only)

## Commit Summary (13 commits)
- All changes committed in 13 atomic commits following plan's commit strategy
- Working tree clean, 13 commits ahead of origin/main
- Git hook blocks Co-authored-by with AI agent references

## Plan Complete — 2026-05-12
