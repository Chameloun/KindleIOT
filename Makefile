GO ?= go
BINARY_ARM = dist/kindle/kindleiot/kindleiot-arm
BINARY_LOCAL = kindleiot
KINDLE_MOUNT ?= /run/media/$(USER)/Kindle

.PHONY: all test build-kindle build-local clean deploy-usb

all: test build-kindle build-local

test:
	$(GO) test -v ./...

build-kindle:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build -trimpath -ldflags="-s -w" -o $(BINARY_ARM) ./cmd/kindleiot

build-local:
	$(GO) build -trimpath -o $(BINARY_LOCAL) ./cmd/kindleiot

deploy-usb: build-kindle
	@test -d "$(KINDLE_MOUNT)" || (echo "Kindle není připojen na $(KINDLE_MOUNT)"; exit 1)
	mkdir -p "$(KINDLE_MOUNT)/kindleiot" "$(KINDLE_MOUNT)/documents"
	cp "$(BINARY_ARM)" "$(KINDLE_MOUNT)/kindleiot/kindleiot-arm"
	cp dist/kindle/kindleiot/start.sh "$(KINDLE_MOUNT)/kindleiot/start.sh"
	@if [ ! -f "$(KINDLE_MOUNT)/kindleiot/config.yaml" ]; then \
		cp config.example.yaml "$(KINDLE_MOUNT)/kindleiot/config.yaml"; \
	fi
	cp dist/kindle/documents/KindleIOT.sh "$(KINDLE_MOUNT)/documents/KindleIOT.sh"
	sync
	@echo "Nasazeno na $(KINDLE_MOUNT)!"

clean:
	rm -f $(BINARY_ARM) $(BINARY_LOCAL)
