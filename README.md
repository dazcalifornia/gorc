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
| `ascii-lines` | A&D, CAS, Mettler, Seca, Charder weight/height columns; anything printing `WT:70.5 HT:170` | serial, SPP |
| `ble-gatt` | Any Bluetooth SIG device: Pulse Oximeter `0x1822`, Weight Scale `0x181D`, Body Composition `0x181B`, Health Thermometer `0x1809`, Blood Pressure `0x1810`, Heart Rate `0x180D` | BLE |

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
