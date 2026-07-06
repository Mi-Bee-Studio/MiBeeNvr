BUILD_DIR  ?= build
BUILD_TARGET ?= $(BUILD_DIR)/mibee-nvr

RPi_HOST ?= user@your-rpi-host
RPi_BIN  := /mnt/data/nvr/bin/mibee-nvr
RPi_SRV  := mibee-nvr

# Docker image registry
DOCKER_REGISTRY ?= ghcr.io/mi-bee-studio/mibeenvr
# Binary version: tag if on tag, else <tag>-<n>-g<hash>[-dirty]; 'dev' outside git.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Linker flags shared by all build targets. Release workflow overrides appVersion via github.ref_name.
LDFLAGS := -s -w -X main.appVersion=$(VERSION)
# -trimpath is a go-build flag (NOT a linker flag) — drops absolute paths from the binary for reproducible builds.
GO_BUILD_FLAGS := -trimpath
CONTAINER_RUNTIME := $(shell command -v docker 2>/dev/null || command -v podman 2>/dev/null)


frontend:
	cd web && npm run build
	rm -rf internal/ui/static/assets
	cp -r web/dist/* internal/ui/static/

build: frontend
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_TARGET) ./cmd/mibee-nvr/



test:
	go test -race ./...

test-verbose:
	go test -race -v ./...

test-short:
	go test -race -short ./...

cross: frontend
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/mibee-nvr-arm64 ./cmd/mibee-nvr/


cross-armv7: frontend
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(GO_BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/mibee-nvr-armv7 ./cmd/mibee-nvr/

lint:
	golangci-lint run

lint-install:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2

clean:
	rm -rf $(BUILD_DIR) web/dist .build-tmp
install: build
	mkdir -p /mnt/data/nvr/bin
	cp $(BUILD_TARGET) /mnt/data/nvr/bin/

install-service: install
	cp deploy/mibee-nvr.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable mibee-nvr

uninstall-service:
	systemctl stop mibee-nvr || true
	systemctl disable mibee-nvr || true
	rm -f /etc/systemd/system/mibee-nvr.service
	systemctl daemon-reload

# ---- Docker / Container Image ----

# Build native-arch container image (multi-stage, requires network for base image pulls)
docker-build:
	$(CONTAINER_RUNTIME) build -t $(DOCKER_REGISTRY):$(VERSION) -f deploy/docker/Dockerfile .

# Build arm64 container image (cross-compiled inside Docker, no QEMU needed).
# The multi-arch Dockerfile cross-compiles the Go binary on the host arch and
# pulls the prebuilt multi-arch base image — no QEMU emulation required.
docker-build-arm64:
	$(CONTAINER_RUNTIME) build --platform linux/arm64 -f deploy/docker/Dockerfile \
		-t $(DOCKER_REGISTRY):$(VERSION)-arm64 .

# Build both amd64 and arm64 images
docker-build-all: docker-build docker-build-arm64

# Push images to registry
docker-push:
	$(CONTAINER_RUNTIME) push $(DOCKER_REGISTRY):$(VERSION)

docker-push-arm64:
	$(CONTAINER_RUNTIME) push $(DOCKER_REGISTRY):$(VERSION)-arm64

docker-push-all: docker-push docker-push-arm64

# Build and push in one shot
docker-release: docker-build-all docker-push-all

# ---- Deploy to RPi ----

deploy: cross
	@echo "=== Deploying to $(RPi_HOST) ==="
	ssh $(RPi_HOST) "sudo systemctl stop $(RPi_SRV) || true"
	ssh $(RPi_HOST) "cp $(RPi_BIN) $(RPi_BIN).bak || true"
	scp $(BUILD_DIR)/mibee-nvr-arm64 $(RPi_HOST):/tmp/mibee-nvr-new
	ssh $(RPi_HOST) "mv /tmp/mibee-nvr-new $(RPi_BIN) && chmod +x $(RPi_BIN)"
	ssh $(RPi_HOST) "sudo systemctl start $(RPi_SRV)"
	@echo "=== Deploy complete. Checking... ==="
	@sleep 2
	$(MAKE) deploy-check

rollback:
	@echo "=== Rolling back on $(RPi_HOST) ==="
	ssh $(RPi_HOST) "sudo systemctl stop $(RPi_SRV) || true"
	ssh $(RPi_HOST) "cp $(RPi_BIN).bak $(RPi_BIN) && chmod +x $(RPi_BIN)"
	ssh $(RPi_HOST) "sudo systemctl start $(RPi_SRV)"
	@echo "=== Rollback complete ==="

deploy-check:
	@ssh $(RPi_HOST) "sudo systemctl is-active $(RPi_SRV)" && echo "✅ Service active" || echo "❌ Service not active"
	@curl -sf http://$(RPi_HOST)/api/health && echo "✅ Health check passed" || echo "❌ Health check failed"

# ---- Model Download ----

download-model-local: build
	@echo "=== Downloading AI model locally ==="
	$(BUILD_TARGET) download-model -config mibee-nvr.yaml

download-model: cross
	@echo "=== Downloading AI model on $(RPi_HOST) ===
	ssh $(RPi_HOST) "sudo systemctl stop $(RPi_SRV) || true"
	$(MAKE) download-model-local
	scp $(BUILD_DIR)/mibee-nvr-arm64 $(RPi_HOST):/tmp/mibee-nvr-new
	ssh $(RPi_HOST) "sudo mkdir -p /mnt/data/nvr/models"
	ssh $(RPi_HOST) "mv /tmp/mibee-nvr-new $(RPi_BIN) && chmod +x $(RPi_BIN)"
	ssh $(RPi_HOST) "$(RPi_BIN) download-model -config /mnt/data/nvr/mibee-nvr.yaml"
	ssh $(RPi_HOST) "sudo systemctl start $(RPi_SRV)"


.PHONY: frontend build test test-verbose test-short cross cross-armv7 lint clean install install-service uninstall-service
.PHONY: docker-build docker-build-arm64 docker-build-all docker-push docker-push-arm64 docker-push-all docker-release
.PHONY: download-model download-model-local deploy rollback deploy-check