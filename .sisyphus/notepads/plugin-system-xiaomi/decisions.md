# Decisions — Plugin System + Xiaomi

## 2026-05-13 Initial
- Plugin approach: Go interface registration (compile-time), NOT Go plugin .so
- V1 CS2-only (no TUTK, no Legacy)
- V1 no build tags (all compiled in), can add `//go:build xiaomi` later
- Xiaomi plugin copies go2rtc code (MIT), adapts to NVR's model.Recorder
- Frontend: backend proxies all Xiaomi cloud API calls
- No token encryption in V1 (document as plaintext)
- Annex B → AVCC conversion: inline ~20 lines, no external h264 package
- Estimated binary size increase: +2MB, memory: +10-15MB per camera
