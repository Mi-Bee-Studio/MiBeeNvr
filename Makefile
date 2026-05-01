BUILD_TARGET ?= mibee-nvr

build:
	CGO_ENABLED=0 go build -o $(BUILD_TARGET) ./cmd/mibee-nvr/

test:
	go test ./... -v

cross:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o mibee-nvr-arm64 ./cmd/mibee-nvr/

lint:
	go vet ./...

clean:
	rm -f mibee-nvr mibee-nvr-arm64

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
