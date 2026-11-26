# Device connector — proof of concept

A React front end and a Go Fiber backend reading medical devices over **BLE**,
**classic Bluetooth** and **USB serial**: pulse oximeters (SpO₂, pulse rate,
plethysmogram), weight and height columns, body-composition scales,
thermometers and blood-pressure monitors.

The point it proves: a browser cannot own a serial port or a Bluetooth radio
reliably across platforms, but a small local Go service can — and one
websocket is enough to make the result feel live in React.

```
  ┌──────────────────────┐        REST  /api/*          ┌───────────────────────────┐
  │  React + Vite  :5173 │ ───────────────────────────▶ │   Go + Fiber        :8080 │
  │                      │ ◀─────────────────────────── │                           │
  │  device picker       │    WebSocket  /ws            │  transports ─▶ decoders   │
  │  live vitals tiles   │    readings · state · log    │       │                   │
  │  waveform canvas     │                              │       ▼                   │
  │  raw frame log       │                              │  session manager ─▶ hub   │
  └──────────────────────┘                              └─────────────┬─────────────┘
                                                                      │
                            ┌─────────────────┬─────────────────┬─────┴────────┐
                            ▼                 ▼                 ▼              ▼
                      USB / serial         BLE GATT      Bluetooth SPP     simulator
                      go.bug.st/serial   tinygo/bluetooth   RFCOMM       (real protocol
                                        CoreBluetooth ·    / /dev/cu.*      bytes)
                                        BlueZ · WinRT
```

## Run it

Needs Go 1.24+ and Node 20+. Nothing else, and **no hardware** — the simulator
emits real protocol bytes through the real decoders.

```sh
make dev-api     # terminal 1: backend on :8080
make dev-web     # terminal 2: UI on http://localhost:5173
```

Open <http://localhost:5173>, stay on the **Simulator** tab, pick a device and
connect. You should see vitals tiles updating, a plethysmogram tracing, and the
raw frames in the traffic log.

Single process instead, serving the built bundle:

```sh
make run         # http://localhost:8080
```

## What it talks to

| Decoder profile | Devices | Transport |
| --- | --- | --- |
| `spo2-bci` | Contec CMS50D+/E/F, Berry BM1000C, Jumper JPD-500 and other BCI-protocol fingertip oximeters | serial, SPP |
| `ascii-lines` | A&D, CAS, Mettler, Seca, Charder weight/height columns; anything printing `WT:70.5 HT:170`; OPD kiosk scales (`W:067.9H:180.0T:36.5`, Adam, SC Stand, TANITA PW-630U) | serial, SPP |
| `ble-gatt` | Any Bluetooth SIG device: Pulse Oximeter `0x1822`, Weight Scale `0x181D`, Body Composition `0x181B`, Health Thermometer `0x1809`, Blood Pressure `0x1810`, Heart Rate `0x180D` | BLE |
| `bp-omron-hbp9010` · `bp-omron-9030` · `bp-omron-9020` | Omron HBP-9010 / USB, HBP-9030, HBP-9020 (CSV) | serial, SPP |
| `bp-accuniq` · `bp-terumo-bp500` | Accuniq, Terumo BP500 (CSV; BP500 is polled every 5 s) | serial, SPP |
| `bp-and-2655` | A&D TM-2655 / -RTS / TM-2657P, Terumo 2655 (0x1E records) | serial, SPP |
| `bp-bio` · `bp-contec-hl7` · `bp-hingmed` · `bp-jawon` · `bp-kenz` · `bp-raycome` · `bp-sy-g100` · `bp-yuwell` | BIO320/750, CONTEC (HL7), Hingmed, Jawon, Kenz, Raycome RBP-7000, SY-G100, Yuwell USB | serial, SPP |
| `thermo-serial` | Serial thermometers: binary `FE FD 1A 01 hi lo 0D 0A` or `T body = 36.29 C` text | serial, SPP |
| `spo2-kscm01` | HealForce kscm01 pulse-oximeter module (`AA 55` frames) | serial, SPP |
| `spo2-yuwell-ble` | Yuwell BLE fingertip oximeters (vendor service `FFE0`, notify `FFE4`/`FFE1`; pulse byte 4, SpO₂ byte 5). Suggested automatically for devices advertising `FFE0` | BLE |
| `ble-glucose` | SIG glucose meters such as Roche Accu-Chek; writes "report last record" to the RACP `0x2A52` on connect. Pair (and enter any PIN) in the OS first | BLE |
| `scale-mi-ble` | Xiaomi Mi body-composition scales (`0x2A9C` in Xiaomi's own layout, weight at bytes 11–12 ÷ 200) | BLE |
| `thermo-htd-ble` | HTD infrared thermometer guns (ASCII on `FFE1`) | BLE |
| `thermo-hid-0c45` | The OPD kiosk's USB thermometer, VID `0C45` PID `7702` (two-report reading, polled every 500 ms) | USB HID (Windows) |
| `hl7-obx` | Bedside monitors pushing HL7 ORU: OBX-3 MDC names (`MDC_PULS_OXIM_SAT_O2`, `MDC_PRESS_BLD_NONINV_SYS`, …) or short codes (`SpO2`, `NIBP_SYS`, `HR`, `RR`, `T1`, …) | HL7 over TCP |

Two transports exist for these OPD devices:

- **USB HID** (Windows only) lists vendor HID interfaces the process can open;
  keyboards and mice are held by Windows and drop out. It talks to `hid.dll`
  directly, so no cgo or hidapi is needed.
- **HL7 monitor (TCP)** listens on `-hl7` (default `:1901`, the OPD port)
  only while a session is open on it; the monitor is the TCP client. Messages
  are framed by MLLP when the monitor uses it, otherwise by 100 ms of silence.
  Each message is acknowledged: `-hl7-ack standard` (default, MLLP, echoes the
  control id), `legacy` (the OPD's fixed string) or `none`. Like the OPD, all
  monitors on one session feed one card, so use one monitor per session.

The `thermo-hid-0c45` decoder follows `GenericHid.dll`'s IL, since the OPD has
no source for it. The OPD's positional HL7 parsers (forms HM and KPHIS) are not
ported: they disagree on what OBX index 7 means and only fit one site's monitor.

The `bp-*`, `thermo-serial` and `spo2-kscm01` profiles are ported from the
SMART EASY OPD kiosk's serial drivers (its `docs/Core/01-Serial-COM-HID.md`),
including each model's line settings (odd parity for Omron 9020, 8N2 for Kenz
and Terumo BP500). Models that send a result with no terminator are framed by
a quiet gap on the line instead of a fixed sleep. The OPD code had no sample
frame for Accuniq, A&D, Terumo, Omron 9020, Hingmed, CONTEC, SY-G100, Kenz,
BIO or Jawon, so those layouts are as documented but untested on hardware.

Readings are normalised before they leave the backend — `spo2`, `pulse_rate`,
`weight`, `height`, `bmi`, `body_fat`, `temperature`, `bp_systolic`, … — with
units converted (grams and millimetres fold into kg and cm; pounds and inches
are preserved as the clinically meaningful unit) and a `quality` flag of
`ok` / `searching` / `artifact` / `no_sensor`, so the UI never contains vendor
protocol knowledge.

## Connecting real hardware

**USB serial.** Plug in, click *Scan* on the **USB / serial** tab. Pick the
port, the profile and the baud rate (19200 for Contec oximeters, 9600 for most
scales), then connect. If nothing arrives, the device is probably waiting for a
command — type it into the session's send box as hex (`7D 81 A1`) or text and
watch the traffic log.

**BLE.** Click *Scan* on the **Bluetooth Low Energy** tab; it listens for six
seconds. Devices advertising a recognised health service are labelled and
preselect the `ble-gatt` profile. On connect the backend subscribes to every
characteristic that accepts notifications — including vendor ones, which is how
you reverse-engineer a device that is not SIG-compliant: watch the hex in the
traffic log.

> **macOS permission.** The first BLE scan asks the OS for Bluetooth access on
> behalf of whatever is running the backend (Terminal, iTerm, your IDE). If you
> miss the prompt, scans return an empty list rather than an error — grant it
> under System Settings → Privacy & Security → Bluetooth and scan again.

**Classic Bluetooth.** This is the one place the platforms genuinely differ,
and the code reflects that rather than pretending otherwise:

- **macOS** has no user-space RFCOMM API. Pair the device in System Settings
  first; macOS then publishes its Serial Port Profile as
  `/dev/cu.<Name>-<Service>` and the backend opens that port. The scan also
  lists paired devices that expose *no* SPP port — marked `no serial port`, and
  not connectable. That is an OS limitation, not a missing feature: such a
  device has to be reached over USB or BLE instead.
- **Linux** opens an `AF_BLUETOOTH`/`BTPROTO_RFCOMM` socket directly to a
  paired address (channel 1 by default), so no `rfcomm bind` is needed.
  Existing `/dev/rfcomm*` bindings still work.
- **Windows** behaves like macOS: pair first, then the device appears as a COM
  port.

Either way the bytes land in the same decoders, so a serial oximeter and a
Bluetooth one share a code path.

## Layout

```
go/
  cmd/connector/          server entrypoint and flags
  internal/transport/     Transport/Conn: serial, ble, btclassic, sim
  internal/decode/        Decoder registry: bci_spo2, ascii_scale, ble_gatt
  internal/btsig/         Bluetooth SIG assigned numbers (shared leaf package)
  internal/manager/       session lifecycle, decode pump, batching
  internal/hub/           websocket fan-out
  internal/api/           Fiber routes and /ws
  internal/model/         normalised Reading and Metric types
gorc/src/
  lib/useConnector.ts     websocket store (useSyncExternalStore)
  lib/api.ts, types.ts    REST client and the backend's JSON contract
  components/             TransportPanel, SessionCard, Waveform, FrameLog
```

## HTTP API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/health` | liveness, session and websocket counts |
| `GET` | `/api/transports` | what this host supports, and why not if it doesn't |
| `GET` | `/api/profiles` | decoder profiles and their applicable transports |
| `GET` | `/api/scan?transport=ble` | discovery (BLE listens ~6 s) |
| `POST` | `/api/sessions` | `{transport, deviceId, profile, baudRate?}` → connect |
| `GET` | `/api/sessions` · `/api/sessions/:id` | session state and latest values |
| `DELETE` | `/api/sessions/:id` | disconnect |
| `DELETE` | `/api/sessions` | drop finished sessions from the list |
| `POST` | `/api/sessions/:id/write` | `{hex}` or `{text}` → send to device |
| `GET` | `/ws` | event stream: `snapshot`, `session`, `readings`, `log` |

## Adding a device

Most devices need a decoder, not a transport. Add a file in
`internal/decode/`, implement `Feed(transport.Frame) []model.Reading`, and
register it:

```go
func init() {
    register(Profile{
        ID:          "my-device",
        Label:       "My oximeter",
        Transports:  []transport.Kind{transport.KindSerial},
        DefaultBaud: 115200,
    }, func() Decoder { return &myDevice{} })
}
```

It appears in the UI's profile dropdown immediately — no frontend change. If
the device needs waking up or keeping alive, also implement `decode.Primer`
(see `bci_spo2.go`, which does both). New metrics only need a constant in
`internal/model`; the UI falls back to a readable label for anything it does
not recognise.

## Design notes worth knowing

- **Backpressure never reaches the radio.** A pulse oximeter produces 60
  samples a second. BLE notification callbacks do a non-blocking push and count
  drops; the session pump batches readings and flushes 20 times a second; the
  hub disconnects a websocket client that falls behind rather than blocking the
  device read loop.
- **Waveforms cost no React renders.** Samples land in a ring buffer outside
  React and the canvas reads it on `requestAnimationFrame`. Scalars and session
  state are published as coalesced snapshots at 8 Hz via
  `useSyncExternalStore`.
- **BLE addresses are per-process on macOS.** CoreBluetooth hands out an opaque
  UUID, not a MAC, and it is only valid for this process run. The transport
  caches the `ScanResult` it was given and connects with that value, which is
  why connecting requires a prior scan — and says so rather than failing
  obscurely.
- **The simulator is not a mock.** It encodes genuine IEEE-11073 SFLOATs and
  BCI frames, so `sim:spo2` exercises the same framing and resync logic a real
  CMS50D+ does. Its encoders and the production decoders are tested against
  each other.

## Limits of this proof of concept

Stated plainly, since these are the things a production build would need:

- No persistence. Readings live in memory; nothing is written to a database or
  an EHR, and there is no HL7/FHIR mapping.
- No authentication, authorisation or audit trail, and the server binds all
  interfaces. Run it on the clinician's own machine, not a shared host.
- CORS allows the Vite dev origin by default (`-cors` to change).
- One session per device at a time; no automatic reconnect after a device drops.
- Decoders cover the SIG profiles and the common BCI/ASCII families. A
  vendor-proprietary device still needs its own decoder — the traffic log is
  there to help you write it.
- Not a medical device, and not validated for clinical use.

## Tests

```sh
make test
```

Covers the decoders against known-good protocol byte vectors (including
pulse rates above 127, split and garbage-prefixed frames, truncated GATT
notifications and imperial units), the full simulator→transport→decoder→hub
path, and the REST surface.
