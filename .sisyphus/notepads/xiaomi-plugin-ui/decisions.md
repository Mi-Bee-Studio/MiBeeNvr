# Decisions

## 2026-05-15 Session Start
- validProtocols: Add "xiaomi" hardcoded with comment (NOT dynamic/plugin-driven)
- Plugin API: Return only {name, protocols} — NO ConfigSchema secrets
- Frontend type fix: xiaomiDevices() returns {devices, message?} not bare array
- Camera form: Hardcode xiaomi-specific fields (NOT generic JSON schema form)
