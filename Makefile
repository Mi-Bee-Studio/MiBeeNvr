BUILD_TARGET ?= mibee-nvr

RPi_HOST := user@your-rpi-host
RPi_BIN  := /mnt/data/nvr/bin/mibee-nvr
RPi_SRV  := mibee-nvr

frontend:
	cd web && npm run build
	cp -r web/dist/* internal/ui/static/

build: frontend
	CGO_ENABLED=0 go build -o $(BUILD_TARGET) ./cmd/mibee-nvr/

test:
	go test ./... -v

cross: frontend
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/

lint:
	go vet ./...

clean:
	rm -f mibee-nvr mibee-nvr-arm64
	rm -rf web/dist

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

deploy: cross
	@echo "=== Deploying to $(RPi_HOST) ==="
	ssh $(RPi_HOST) "sudo systemctl stop $(RPi_SRV) || true"
	ssh $(RPi_HOST) "cp $(RPi_BIN) $(RPi_BIN).bak || true"
	scp mibee-nvr-arm64 $(RPi_HOST):/tmp/mibee-nvr-new
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
