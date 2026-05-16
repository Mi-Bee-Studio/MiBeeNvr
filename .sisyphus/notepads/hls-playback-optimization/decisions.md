# Decisions

## 2026-05-13
- SegmentCount default: 7 (from 3, gives 14s playlist window)
- writeBufSize default: 100 (from 40, gives ~5s buffer at 20fps)
- enableWorker: true (modern browsers only)
- maxBufferLength: 15s (from 5s)
- RecoverMediaError debounce: 500ms
- Zombie detection: readyState=0 for 10s or no FRAG_LOADED for 30s
- Destroy+recreate after 3 failed recovers in 5s
- Max recreate attempts: 2 before snapshot fallback
