# Configuration Reference (`config.yaml`)

All settings live in a single YAML file (default `config.yaml`). Changes made through the built-in Web Configurator (`:8080`) or `POST /api/config` are validated, persisted back to `config.yaml`, and applied live without restarting KindleIOT.

See [`config.example.yaml`](../config.example.yaml) for a ready-to-use template.

---

## Top-Level Sections

### `mqtt` — Primary & Secondary Brokers

```yaml
mqtt:
  id: default
  name: Main Broker
  broker: tcp://192.168.1.10:1883     # tcp://, ssl://, mqtt://, mqtts://, or wss://
  client_id: kindleiot-kt4
  username: ""
  password: ""
  insecure_tls: false
  keep_alive_sec: 30
  base_topic: kindleiot/panel
  homeassistant_discovery: true
  discovery_prefix: homeassistant

  # Optional extra broker connections (widgets reference them via broker_id):
  brokers:
    - id: cloud
      name: HiveMQ Cloud
      broker: ssl://example.s1.eu.hivemq.cloud:8883
      username: kindle
      password: secret
```

- URL schemes `mqtt://` are normalized to `tcp://`, and `mqtts://`, `tls://`, or bare `*.hivemq.cloud` hostnames / `:8883` ports normalize to `ssl://`.
- Embedded Mozilla root CA certificates enable TLS out of the box on older Kindle firmware. Set `insecure_tls: true` only for self-signed LAN brokers.

#### Panel Telemetry & Control Topics (`<base_topic>/...`)

| Direction | Topic | Payload / Purpose |
| :--- | :--- | :--- |
| Publish (LWT + online) | `<base_topic>/status` | `online` / `offline` (retained, QoS 1) |
| Publish | `<base_topic>/battery` | Battery percentage (`0`–`100`, retained) |
| Publish | `<base_topic>/charging` | `ON` / `OFF` (retained) |
| Publish | `<base_topic>/ip` | Current Kindle IPv4 address (retained) |
| Publish | `<base_topic>/brightness` | Frontlight level `0`–`24` (retained) |
| Subscribe | `<base_topic>/brightness/set` | Integer `0`–`24` to adjust Kindle frontlight |
| Subscribe | `<base_topic>/screen/refresh` | Any message triggers a full anti-ghosting e-ink flash |
| Subscribe | `<base_topic>/notify` | Displays text notice on screen, wakes backlight & exits standby for 15s |

When `homeassistant_discovery: true`, KindleIOT publishes MQTT Discovery payloads under `<discovery_prefix>/sensor/...`, `binary_sensor/...`, and `number/...` for Battery, Charging, and Frontlight (`0..24`).

---

### `homekit` — Apple HomeKit Bridge

```yaml
homekit:
  enabled: true
  pin: "00102003"       # Must be exactly 8 numeric digits
  port: 51827
  store_dir: homekit-db
```

Any widget with `homekit: true` appears in the HomeKit bridge:
- `type: switch` → exposed as `lightbulb` (default), `switch`, or `outlet` (controlled by `homekit_type`)
- `type: sensor`, `gauge`, or `chart` → exposed as a HomeKit Temperature Sensor (`-50` .. `100`)

Switches using `state_topic: local/switch/<id>` with no `command_topic` persist their state locally and synchronize bidirectionally between Kindle taps, Apple Home, Countdown Timers, Macros, and Daily Schedules.

---

### `weather` — Open-Meteo Forecast & Czech Namedays

```yaml
weather:
  enabled: true
  location_name: Prague
  latitude: 50.0755
  longitude: 14.4378
```

Fetches current weather, 24-hour hourly temperatures/precipitation probabilities, and a 7-day daily forecast from Open-Meteo every 30 minutes (no API key needed). Czech calendar namedays (*svátky*) are resolved offline from an embedded table and shown automatically when `device.language: cs`.

---

### `calendar` — iCal (`.ics` / `webcal://`) Feed Reader

```yaml
calendar:
  enabled: true
  urls:
    - https://calendar.google.com/calendar/ical/.../basic.ics
    - webcal://p123-caldav.icloud.com/published/2/...
```

- Polls all configured feeds every 15 minutes (`webcal://` URLs are converted to `https://` automatically).
- Supports all-day (`VALUE=DATE`), local/TZID, and UTC (`Z`) `VEVENT`s, line unfolding, backslash unescaping, and `RRULE` `FREQ=WEEKLY` / `FREQ=YEARLY` projections within the next 14 days (up to 15 upcoming events sorted chronologically).

---

### `schedules` — On-Device Daily Automation Schedules

```yaml
schedules:
  - id: morning_light
    name: Morning Light
    enabled: true
    hour: 7               # 0..23
    minute: 30            # 0..59
    days: [1, 2, 3, 4, 5] # ISO weekday 1=Mon..7=Sun (empty = every day)
    widget_id: living_lamp
    state: ON             # ON, OFF, or TOGGLE
    # Or publish directly to MQTT when widget_id is omitted:
    # broker_id: ""
    # topic: home/scene/set
    # payload: morning
```

Evaluated locally on the Kindle every 5 seconds with single-fire-per-minute deduplication. Triggers `OnWidgetChanged` so HomeKit accessories stay synchronized.

---

### `device` — Display, Orientation, Dark Mode, Power & Security

```yaml
device:
  title: KindleIOT
  language: en                # en (English) | cs (Čeština)
  width: 600
  height: 800
  orientation: portrait       # portrait | landscape (800x600) | landscape_cw (800x600 rotated 180°)
  theme_mode: light           # light | dark | auto_night
  prevent_sleep: true         # Keeps Kindle awake via lipc preventScreenSaver
  initial_brightness: -1      # -1 uses day/night target, or fixed 0..24
  backlight_timeout_sec: 60   # 0 disables; otherwise sets frontlight to 0 after N idle seconds
  standby_timeout_sec: 300    # 0 disables; enters Standby clock/weather/4-card view after N seconds
  day_brightness: 14          # 0..24 frontlight level restored upon tap/wake
  night_mode_enabled: true
  night_brightness: 3         # Lower frontlight restored during night window
  night_start_hour: 22        # 0..23
  night_end_hour: 6           # 0..23
  pin_code: ""                # 4-8 digits enables keypad lock on marked widgets / quick lock
  pin_lock_standby: false     # Require PIN before waking from standby
  telemetry_interval_sec: 60
  full_refresh_every: 20      # Full B/W e-ink flash every N renders
```

- `language` (`en` default, or `cs`) sets the language for both the Kindle e-ink UI and the default Web Configurator interface (also switchable live via the `EN | CZ` header toggle).
- When `theme_mode: auto_night`, the UI inverts to Dark Mode automatically during `night_start_hour..night_end_hour`. You can also toggle Light/Dark manually for the current session from the top gear (`*` / Settings) overlay via the `◐` button.
- Sensor alerts (`alert_enabled: true`) automatically wake both Standby and dimmed backlight whenever a threshold is violated.

---

### `web` & `pages`

```yaml
web:
  listen: ":8080"
```

Each entry in `pages` has:
- `id`, `title`: Tab identifier and bottom bar label.
- `type`: `grid` (default) or `floorplan`.
- `hidden`: `true` omits the page from the Kindle bottom tab bar while keeping its MQTT topics, alerts, standby widgets, schedules, and HomeKit accessories active (`false` default).
- `columns`: `1`, `2`, or `3` (clamped `1..3`; 3 columns works great in `landscape`).
- `widgets`: List of widgets (cards with `span` `1..columns`).
- `floorplan`: When `type: floorplan`, contains `shapes` (`kind: room | wall | door | text` in normalized `0..1000` coordinates) and `pins` (`x`, `y` in `0..1000` wrapping any `WidgetConfig`).

---

## Widget Types Reference

| Type | Key Fields | Behavior |
| :--- | :--- | :--- |
| **`switch`** | `state_topic`, `command_topic`, `payload_on/off`, `value_on/off`, `state_json_path`, `optimistic`, `homekit`, `homekit_type` | Inverted black card when ON with physical pill toggle. Syncs bidirectionally with Apple Home if `homekit: true`. |
| **`button`** | `command_topic`, `payload`, `confirm`, `macro_actions[]` | Triggers a single MQTT command OR executes a list of switch actions (`widget_id` + `ON`/`OFF`/`TOGGLE`, or raw `topic`/`payload`). `confirm: true` requires a 2nd confirmation tap within 8s. |
| **`stepper`** | `state_topic`, `command_topic`, `command_template`, `min`, `max`, `step`, `precision`, `unit` | Large `−` / `+` buttons with progress bar; substitutes `{{value}}` in `command_template`. |
| **`sensor`** | `state_topic`, `state_json_path`, `precision`, `unit`, `format`, `secondary_*` (`secondary_precision`), `value_map`, `stale_seconds`, `show_in_standby`, `alert_*` | Numeric/text reading with optional decimal rounding (`precision` / `secondary_precision`: `0..6`, omit for auto), optional secondary pill (e.g. humidity), age/staleness warning (`⚠`), and tap-to-open history modal with min/avg/max. |
| **`gauge`** | `state_topic`, `state_json_path`, `min`, `max`, `precision`, `unit`, `alert_*` | Half-circle dial needle gauge (`0..max`) with scale ticks; opens detail history modal on tap. |
| **`chart`** | `state_topic`, `state_json_path`, `min`, `max`, `precision`, `unit`, `alert_*` | Dithered historical area sparkline (`max 120` buffered topic updates); opens detail history modal on tap. |
| **`select`** | `state_topic`, `command_topic`, `options: [{label, payload}]` | Horizontal row of preset mode buttons highlighting the active payload. |
| **`weather`** | `label`, `span` | Shows location, current temp/conditions, wind, today's + next 2 days min/max, and Czech nameday. Tap opens 24h hourly curve + 7-day forecast modal. |
| **`timer`** | `target_widget_id`, `target_state`, `timer_presets` (`[5,15,30]`), `command_topic`, `payload` | Preset countdown buttons (`5m`, `15m`, `30m`). When active, displays live countdown, progress bar, `+5m`, and `✕ Cancel` / `✕ Zrušit`. |
| **`calendar`** | `label`, `span` | Shows next 3 upcoming events from configured iCal feeds; tap opens full 14-day event modal. |

Common optional widget fields: `broker_id`, `icon` (`auto`, `light`, `thermometer`, `drop`, `bolt`, `plug`, `door`, `home`, `fan`, `sun`, `clock`, `calendar`, `power`), `subtitle`, `span`, `qos` (`0..2`), `retain`, `pin_protected`, `show_in_standby`.
