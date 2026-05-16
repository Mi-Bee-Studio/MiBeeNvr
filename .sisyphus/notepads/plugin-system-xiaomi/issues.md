# Issues — Plugin System + Xiaomi

## Resolved
- F2 review found 2 missing MIT headers in crypto_test.go and plugin.go → fixed in commit f3a8251
- F4 reviewer couldn't find E2E test file (gitignored directory) → false positive, file exists at 17KB/449 lines
- T1 `quick` agent (nvidia-nemotron-nano) produced zero code → orchestrator implemented directly
- T2 `quick` agent created broken crypto_test.go → orchestrator fixed manually
- T10 `quick` agent produced zero changes → orchestrator implemented i18n directly

## Open (user action required)
- F3 full Xiaomi account integration test requires real user credentials — automated verification confirmed API endpoints respond correctly
- xiaomi_8p_balcony camera reconnecting (pre-existing issue, not related to plugin system)
