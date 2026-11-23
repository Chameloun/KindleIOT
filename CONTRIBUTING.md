# Contributing to KindleIOT

Contributions, bug reports, hardware compatibility notes (other Kindle models / firmware versions), and pull requests are welcome!

## Prerequisites

- **Go** matching `go.mod` (`go 1.26.0` or newer; built and tested with Go `1.27.x`)
- `make` (optional convenience targets)
- No C compiler or external shared libraries are required (`CGO_ENABLED=0`)

## Development Workflow

### 1. Run Unit Tests & Static Checks
```bash
go vet ./...
go test -count=1 -v ./...
```

### 2. Generate Rendered UI PNG Previews
The UI test suite renders every e-ink screen and modal into 8-bit grayscale PNG images when `KINDLEIOT_PREVIEW_DIR` is set:
```bash
mkdir -p /tmp/kindleiot-previews
KINDLEIOT_PREVIEW_DIR=/tmp/kindleiot-previews go test -count=1 ./internal/ui/...
```
Always inspect generated PNGs before opening a UI PR to verify text truncation, alignment, contrast, and Dark Mode inversion.

### 3. Build Binaries
```bash
# Desktop binary (runs on X11 or with -headless web simulator)
make build-local

# Kindle ARMv7 static binary (dist/kindle/kindleiot/kindleiot-arm)
make build-kindle
```

### 4. Local Live Preview Without a Kindle
```bash
cp config.example.yaml config.yaml
./kindleiot -config config.yaml -headless
```
Open `http://127.0.0.1:8080` in your browser to use the drag-and-drop builder, live screen mirror, and MQTT topic inspector.

## Important UI & Kindle Constraints

1. **DejaVu Sans BMP-Only Glyphs in `internal/ui/*.go`:**
   Embedded `DejaVuSans.ttf` and `DejaVuSans-Bold.ttf` do **not** contain SMP Unicode emojis (`U+10000+`, such as `🌙`, `⏱`, `📅`). Using them in Go UI rendering produces square missing-glyph boxes (`□`) on e-ink. Restrict `internal/ui/*.go` strings to BMP glyphs (`☀`, `↻`, `✕`, `✓`, `⚠`, `●`, `○`, `☾`, `◐`, `▶`, `◀`, `▲`, `▼`, `⌫`, `•`, `°`) or draw vector shapes in [`internal/ui/graphics.go`](internal/ui/graphics.go). Browser-only UI strings in `internal/web/static/index.html` may use standard emojis.
2. **E-Ink Contrast & Ghosting:**
   Favor crisp 1px/2px black borders, high-contrast pills, subtle shadows, and 2×2 ordered dithering over fine anti-aliased gradients. Avoid rapid redraw loops (< 5s) when idle.
3. **ARMv7 Kindle Kernel Quirks:**
   Keep `CGO_ENABLED=0` and preserve `export GODEBUG="asyncpreemptoff=1"` in `dist/kindle/kindleiot/start.sh` to avoid SIGURG preemption instability on older Kindle Linux kernels.
4. **Never Commit Local State:**
   Ensure `config.yaml`, `homekit-db/`, and compiled binaries remain untracked by Git.
