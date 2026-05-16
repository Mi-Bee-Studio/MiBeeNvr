# Deploy to Raspberry Pi

## TL;DR
> Cross-compile ARM64 binary, SCP to mickey@192.168.63.31, restart mibee-nvr service.

## Context
- Target: `mickey@192.168.63.31` (RPi 3B, ARM64)
- Binary path on RPi: `/mnt/data/nvr/bin/mibee-nvr`
- Config path on RPi: `/mnt/data/nvr/mibee-nvr.yaml`
- Service: `mibee-nvr.service` (systemd)
- Frontend already built and embedded in Go binary via `internal/ui/static/`

## TODOs

- [x] 1. Cross-compile ARM64 binary

  **What to do**:
  - Run `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/`
  - Verify binary: `file mibee-nvr-arm64` should show ELF ARM64

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

- [x] 2. Deploy binary to RPi and restart service

  **What to do**:
  - SCP binary: `scp mibee-nvr-arm64 mickey@192.168.63.31:/mnt/data/nvr/bin/mibee-nvr`
  - SSH restart: `ssh mickey@192.168.63.31 sudo systemctl restart mibee-nvr`
  - Verify: `ssh mickey@192.168.63.31 sudo systemctl status mibee-nvr`

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

## Success Criteria
- [ ] `file mibee-nvr-arm64` → ELF 64-bit LSB executable, ARM aarch64
- [ ] Service active and running on RPi
- [ ] Web UI accessible at http://192.168.63.31:9090
