BUILD_TARGET ?= mibee-nvr

RPi_HOST ?= user@your-rpi-host
RPi_BIN  := /mnt/data/nvr/bin/mibee-nvr
RPi_SRV  := mibee-nvr

# Plugin build targets
XIAOMI_PLUGIN_TARGET   := plugins/xiaomi/xiaomi-plugin
XIAOMI_PLUGIN_ARM64    := plugins/xiaomi/xiaomi-plugin-arm64
XIAOMI_PLUGIN_SRC      := ./plugins/xiaomi/cmd/xiaomi-plugin/
XIAOMI_PLUGIN_RPI_DIR  := /mnt/data/nvr/plugins/xiaomi
XIAOMI_PLUGIN_RPI_BIN  := $(XIAOMI_PLUGIN_RPI_DIR)/xiaomi-plugin

# Docker image registry
DOCKER_REGISTRY ?= ghcr.io/mi-bee-studio/mibeenvr
VERSION := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
CONTAINER_RUNTIME := $(shell command -v docker 2>/dev/null || command -v podman 2>/dev/null)

proto:
	@echo "Proto generation not yet configured"

frontend:
	cd web && npm run build
	rm -rf internal/ui/static/assets
	cp -r web/dist/* internal/ui/static/

build: frontend plugins
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BUILD_TARGET) ./cmd/mibee-nvr/

plugin-xiaomi:
	@if [ -d ./plugins/xiaomi/cmd/xiaomi-plugin ]; then \
		CGO_ENABLED=0 go build -ldflags="-s -w" -o $(XIAOMI_PLUGIN_TARGET) $(XIAOMI_PLUGIN_SRC); \
	else \
		echo "Skipping xiaomi-plugin (cmd directory not found)"; \
	fi

plugins: plugin-xiaomi

test:
	go test ./... -v

cross: frontend plugins-cross
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o mibee-nvr-arm64 ./cmd/mibee-nvr/

plugin-xiaomi-arm64:
	@if [ -d ./plugins/xiaomi/cmd/xiaomi-plugin ]; then \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(XIAOMI_PLUGIN_ARM64) $(XIAOMI_PLUGIN_SRC); \
	else \
		echo "Skipping xiaomi-plugin-arm64 (cmd directory not found)"; \
	fi

plugins-cross: plugin-xiaomi-arm64
cross-armv7: frontend
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o mibee-nvr-armv7 ./cmd/mibee-nvr/

lint:
	go vet ./...

clean:
	rm -f mibee-nvr mibee-nvr-arm64 mibee-nvr-armv7 $(XIAOMI_PLUGIN_TARGET) $(XIAOMI_PLUGIN_ARM64)
	rm -rf web/dist .build-tmp

install: build
	mkdir -p /mnt/data/nvr/bin
	cp mibee-nvr /mnt/data/nvr/bin/

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
	$(CONTAINER_RUNTIME) build -t $(DOCKER_REGISTRY):$(VERSION) .

# Build arm64 container image using host cross-compilation (no QEMU needed)
# Uses scratch base image — Go binary is statically linked, no runtime deps
docker-build-arm64: cross
	@mkdir -p .build-tmp
	cp mibee-nvr-arm64 .build-tmp/mibee-nvr
	$(CONTAINER_RUNTIME) build --platform linux/arm64 -f Dockerfile.arm64 \
		-t $(DOCKER_REGISTRY):$(VERSION)-arm64 .
	@rm -rf .build-tmp

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

deploy: cross plugins-cross
	@echo "=== Deploying to $(RPi_HOST) ==="
	ssh $(RPi_HOST) "sudo systemctl stop $(RPi_SRV) || true"
	ssh $(RPi_HOST) "cp $(RPi_BIN) $(RPi_BIN).bak || true"
	scp mibee-nvr-arm64 $(RPi_HOST):/tmp/mibee-nvr-new
	ssh $(RPi_HOST) "mv /tmp/mibee-nvr-new $(RPi_BIN) && chmod +x $(RPi_BIN)"
	ssh $(RPi_HOST) "mkdir -p $(XIAOMI_PLUGIN_RPI_DIR)"
	scp $(XIAOMI_PLUGIN_ARM64) $(RPi_HOST):/tmp/xiaomi-plugin-new
	ssh $(RPi_HOST) "mv /tmp/xiaomi-plugin-new $(XIAOMI_PLUGIN_RPI_BIN) && chmod +x $(XIAOMI_PLUGIN_RPI_BIN)"
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
