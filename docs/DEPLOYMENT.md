# Kindle Deployment & Setup Guide

## Tested Hardware & Jailbreak Stack

KindleIOT has been built and verified on:
- **Device:** Amazon Kindle 10th Gen (`KT4`, ARMv7 `armhf`)
- **Firmware:** `5.18.1.1.1`
- **Launch Method:** **KPM / Scriptlets (`hdnext`)** running a native X11 application inside Kindle Awesome WM

> [!NOTE]
> Because `kindleiot-arm` is compiled with `CGO_ENABLED=0` and embeds its own X11 client (`github.com/jezek/xgb`), DejaVu Sans fonts, and Mozilla root CA certificates, it has zero runtime shared-library dependencies and is expected to work on most ARMv7 (`GOARM=7`) jailbroken Kindles running X11 + `lipc`.

---

## Filesystem Layout on Kindle

When deployed over USB, Kindle user storage (`KINDLE_MOUNT`, e.g. `/run/media/$USER/Kindle`) maps to `/mnt/us` inside the Kindle Linux environment:

```text
/mnt/us/
├── documents/
│   └── KindleIOT.sh           # Scriptlet entry visible as "KindleIOT" book in Library
└── kindleiot/
    ├── kindleiot-arm          # Static ARMv7 Go binary (CGO_ENABLED=0)
    ├── start.sh               # Wrapper script handling env, firewall, Wi-Fi, PID file & logging
    ├── config.yaml            # User configuration (auto-created if missing on first run)
    ├── homekit-db/            # Persisted Apple HomeKit pairings and switch states (created at runtime)
    └── kindleiot.log          # Application & startup output log
```

---

## Deploying from GitHub Releases (Prebuilt)

1. Download `kindleiot-<version>-kindle-armhf.zip` from [GitHub Releases](https://github.com/Chameloun/KindleIOT/releases).
2. Connect the Kindle to your computer via USB.
3. Extract the archive contents directly into the root of the Kindle USB drive (`kindleiot/` and `documents/` folders).
4. *(Optional)* Copy `kindleiot/config.example.yaml` to `kindleiot/config.yaml` and edit `mqtt.broker`, `weather` coordinates, or `device` settings. If skipped, KindleIOT initializes a default configuration on startup so you can configure everything visually in your browser.
5. Safely eject the Kindle USB drive, wait a few seconds for indexing, and tap **KindleIOT** in your Kindle Library.

---

## Building & Deploying from Source

Requirements on your workstation: **Go (`1.26+`, tested on `1.27`)** and **`make`**.

```bash
# 1. Run unit tests
make test

# 2. Cross-compile ARMv7 binary -> dist/kindle/kindleiot/kindleiot-arm
make build-kindle

# 3. Deploy onto USB-mounted Kindle (defaults to /run/media/$USER/Kindle)
make deploy-usb
```

If your OS mounts USB storage elsewhere (for example macOS `/Volumes/Kindle` or `/media/$USER/Kindle`), override `KINDLE_MOUNT`:

```bash
make deploy-usb KINDLE_MOUNT=/media/$USER/Kindle
```

> `make deploy-usb` never overwrites an existing `config.yaml` on the Kindle.

---

## What Happens at Startup on Kindle

[`dist/kindle/documents/KindleIOT.sh`](../dist/kindle/documents/KindleIOT.sh) invokes `/mnt/us/kindleiot/start.sh` via the Scriptlet runner (`# DontUseFBInk` preserves the X11 stack).

Inside [`dist/kindle/kindleiot/start.sh`](../dist/kindle/kindleiot/start.sh):
1. Stops any previously running instance via `/mnt/us/kindleiot/kindleiot.pid`.
2. Sets `export GODEBUG="asyncpreemptoff=1"` — **critical on older Kindle ARMv7 Linux kernels** to avoid Go async signal preemption (`SIGURG`) crashes.
3. Opens firewall rules (`iptables`) for:
   - `8080/tcp` — Web UI & screen mirror (or the configured `web.listen` port, also maintained dynamically by KindleIOT)
   - `51827/tcp` — Apple HomeKit Accessory Protocol (`homekit.port`)
   - `5353/udp` — mDNS / Bonjour (`avahi`/HAP discovery)
4. Disables Wi-Fi power saving (`iwconfig wlan0 power off`) and tunes TCP keepalives so HomeKit and MQTT commands arrive immediately without wake latency.
5. Launches `kindleiot-arm -config /mnt/us/kindleiot/config.yaml -fullscreen` and logs stdout/stderr to `/mnt/us/kindleiot/kindleiot.log`.

Hardware calls in [`internal/kindle/system.go`](../internal/kindle/system.go) use Kindle's native `lipc-get-prop` / `lipc-set-prop` (`com.lab126.powerd` and `com.lab126.wifid`) to query battery/charging telemetry, inhibit the Kindle OS screensaver (`preventScreenSaver`), and adjust frontlight intensity (`flIntensity`, `0..24`).

---

## Pairing with Apple Home (HomeKit)

1. Open the web configurator at `http://<KINDLE-IP>:8080` (check the IP in your router or on `<base_topic>/ip`).
2. Mark switches (`homekit: true`, choose `lightbulb`, `switch`, or `outlet`) or temperature sensors/gauges to expose to HomeKit. You can also use local switches (`state_topic: local/switch/<id>`, empty `command_topic`) for pure Apple Home automation triggers.
3. Open **Apple Home (Domácnost)** on iOS/macOS → **+** → **Add Accessory** → **More options...**.
4. Select **IoT Panel Bridge** and enter the 8-digit pairing code (`homekit.pin`, default `00102003`).
5. To link a virtual Kindle switch to another existing HomeKit device (for example a HomeKit bulb), create a Home automation in Apple Home: *When accessory turns on/off → turn on/off target light*.

To unpair / re-pair cleanly, click **Resetovat spárování s Apple Home** in the Web UI Settings tab (`POST /api/homekit/reset`) or stop KindleIOT and remove `/mnt/us/kindleiot/homekit-db`.

---

## Local Desktop Usage & CLI Flags

```text
./kindleiot [flags]
  -config string    Path to YAML configuration file (default "config.yaml")
  -headless         Run without X11 window (web preview + MQTT + HomeKit only)
  -fullscreen       Request X11 fullscreen on desktop
```

- When `-headless` is passed, you can interact with the full panel solely through `http://127.0.0.1:8080` (**Živý náhled** tab).
- On desktop with X11 enabled, `device.orientation: landscape` opens an `800×600` desktop window directly; on physical Kindle hardware (`hw.IsKindle() == true`), KindleIOT opens the native `600×800` framebuffer and performs software rotation + inverse touch mapping automatically.

---

## Troubleshooting

- **Panel launches to an older config or won't connect to LAN ports:** Check `/mnt/us/kindleiot/kindleiot.log` via USB or SSH (`KUAL`/USBNetwork). Verify Kindle Wi-Fi is connected and not in Airplane Mode.
- **No screen refresh after idle or sleep:** Set `device.prevent_sleep: true` when powered via USB wall charger so KindleOS doesn't suspend the CPU. Use `device.backlight_timeout_sec` and `device.standby_timeout_sec` to keep power/frontlight comfortable at night without suspending MQTT/HomeKit.
- **HomeKit says "Accessory Not Reachable":** Confirm client iPhone/Home Hub and Kindle are on the same subnet/VLAN (mDNS `udp/5353` multicast does not cross routers without an mDNS reflector) and check `iptables` isn't blocking `51827/tcp` or `5353/udp`.
