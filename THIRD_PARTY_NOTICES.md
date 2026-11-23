# Third-Party Notices

KindleIOT bundles or depends on the following third-party software and assets:

## Embedded Assets

### DejaVu Sans & DejaVu Sans Bold (`internal/ui/fonts/`)
- **Files:** `DejaVuSans.ttf`, `DejaVuSans-Bold.ttf`
- **Copyright:** Bitstream, Inc. / Tavmjong Bah / DejaVu fonts contributors
- **License:** Permissive Bitstream Vera / Arev / Public Domain font license (<https://dejavu-fonts.github.io/License.html>)

### Embedded TLS Root CA Bundle (`internal/mqtt/certs/ca-certificates.crt`)
- **Source:** Mozilla NSS / Debian `ca-certificates`
- **Purpose:** Enables TLS (`ssl://`) MQTT connections as well as HTTPS calls to Open-Meteo and `.ics` calendar feeds on Kindle devices lacking an updated system CA bundle.
- **License:** Mozilla Public License 2.0 (MPL-2.0)

---

## Go Dependencies (`go.mod`)

| Module | Purpose | License |
| --- | --- | --- |
| `github.com/brutella/hap` | Pure-Go Apple HomeKit Accessory Protocol (HAP) server | Apache-2.0 |
| `github.com/eclipse/paho.mqtt.golang` | MQTT v3.1.1 client library | EPL-2.0 / EDL-1.0 |
| `github.com/jezek/xgb` | Pure-Go X11 protocol binding (`CGO_ENABLED=0`) | BSD-3-Clause |
| `golang.org/x/image` | Grayscale image helpers and OpenType font rendering | BSD-3-Clause |
| `gopkg.in/yaml.v3` | YAML configuration parser and serializer | MIT / Apache-2.0 |
