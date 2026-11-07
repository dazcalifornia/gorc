import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../lib/api'
import type { Availability, DeviceInfo, Profile, TransportKind } from '../lib/types'

const BAUD_RATES = [1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200]
const FALLBACK_BAUD = 9600

/** Transports cheap and safe enough to enumerate without being asked. */
const AUTO_SCAN: TransportKind[] = ['sim', 'serial']
/** The simulator opens first so the console is usable with no hardware. */
const INITIAL_TRANSPORT: TransportKind = 'sim'

/**
 * TransportPanel is the discovery half of the console: pick a transport, scan,
 * choose a decoder profile, connect.
 *
 * It does not report the new session upwards: the backend broadcasts it over
 * the websocket, so the sessions panel learns about it the same way it would
 * if another operator had connected the device.
 */
export function TransportPanel() {
  const [transports, setTransports] = useState<Availability[]>([])
  const [profiles, setProfiles] = useState<Profile[]>([])
  const [active, setActive] = useState<TransportKind>(INITIAL_TRANSPORT)
  const [devices, setDevices] = useState<DeviceInfo[]>([])
  const [scanning, setScanning] = useState(false)
  const [error, setError] = useState<string>()
  const [selected, setSelected] = useState<string>()
  const [connecting, setConnecting] = useState(false)

  // Profile and baud are derived from the selected device, with these holding
  // only an explicit operator override. Deriving rather than syncing is what
  // keeps a scan result's suggested profile from fighting the dropdown.
  const [profileChoice, setProfileChoice] = useState<string>()
  const [baudChoice, setBaudChoice] = useState<number>()


  const current = transports.find((t) => t.kind === active)
  const device = devices.find((d) => d.id === selected)
  const needsBaud = active === 'serial' || active === 'bt-classic'

  // Only offer decoders that make sense for the selected transport.
  const usable = useMemo(
    () => profiles.filter((p) => p.transports.includes(active)),
    [profiles, active],
  )

  // A scan result that names its own profile preselects it — that is the point
  // of reading the advertised GATT services.
  const profile =
    profileChoice && usable.some((p) => p.id === profileChoice)
      ? profileChoice
      : (device?.suggestedProfile ?? usable[0]?.id ?? '')

  const selectedProfile = profiles.find((p) => p.id === profile)
  const baud = baudChoice ?? selectedProfile?.defaultBaud ?? FALLBACK_BAUD

  const scan = useCallback(async (kind: TransportKind) => {
    setScanning(true)
    setError(undefined)
    try {
      setDevices(await api.scan(kind))
    } catch (e) {
      setError((e as Error).message)
      setDevices([])
    } finally {
      setScanning(false)
    }
  }, [])

  // Load the capability lists, then enumerate the default transport. Both
  // happen after an await, so neither re-renders synchronously from the effect.
  useEffect(() => {
    void (async () => {
      try {
        const [t, p] = await Promise.all([api.transports(), api.profiles()])
        setTransports(t)
        setProfiles(p)
        if (AUTO_SCAN.includes(INITIAL_TRANSPORT)) await scan(INITIAL_TRANSPORT)
      } catch (e) {
        setError((e as Error).message)
      }
    })()
  }, [scan])

  const switchTransport = (kind: TransportKind) => {
    setActive(kind)
    setDevices([])
    setSelected(undefined)
    setProfileChoice(undefined)
    setBaudChoice(undefined)
    setError(undefined)
    // Enumerating serial ports and the simulator is instant and side-effect
    // free, so do it on arrival. BLE is not: it takes seconds of radio time
    // and prompts for OS permission, so it waits for an explicit Scan.
    if (AUTO_SCAN.includes(kind)) void scan(kind)
  }

  const connect = async () => {
    if (!device || !profile) return
    setConnecting(true)
    setError(undefined)
    try {
      await api.open({
        transport: active,
        deviceId: device.id,
        address: device.address || undefined,
        profile,
        baudRate: needsBaud ? baud : undefined,
      })
      setSelected(undefined)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setConnecting(false)
    }
  }

  return (
    <section className="panel">
      <h2>Devices</h2>

      <div className="tabs" role="tablist">
        {transports.map((t) => (
          <button
            key={t.kind}
            role="tab"
            aria-selected={t.kind === active}
            className={t.kind === active ? 'tab active' : 'tab'}
            disabled={!t.available}
            onClick={() => switchTransport(t.kind)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {current && <p className="note">{current.note}</p>}

      <div className="row">
        <button className="primary" onClick={() => void scan(active)} disabled={scanning}>
          {scanning ? 'Scanning…' : 'Scan'}
        </button>
        <span className="muted">
          {scanning
            ? ''
            : devices.length === 0
              ? 'no devices found'
              : `${devices.length} device${devices.length === 1 ? '' : 's'}`}
        </span>
      </div>

      {error && <p className="error">{error}</p>}

      <ul className="devices">
        {devices.map((d) => (
          <li key={d.id}>
            <button
              className={d.id === selected ? 'device selected' : 'device'}
              onClick={() => {
                setSelected(d.id)
                setProfileChoice(undefined)
                setBaudChoice(undefined)
              }}
            >
              <span className="device-name">{d.name}</span>
              <span className="device-meta">
                {d.address || d.details?.address || d.id}
                {d.rssi ? ` · ${d.rssi} dBm` : ''}
              </span>
              {d.details?.profiles && <span className="badge">{d.details.profiles}</span>}
              {d.details?.emits && <span className="device-meta">{d.details.emits}</span>}
              {d.details?.link === 'paired, no SPP port' && (
                <span className="badge warn">no serial port</span>
              )}
            </button>
          </li>
        ))}
      </ul>

      {device && (
        <div className="connect">
          <label>
            Decoder profile
            <select value={profile} onChange={(e) => setProfileChoice(e.target.value)}>
              {usable.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </label>
          {needsBaud && (
            <label>
              Baud rate
              <select value={baud} onChange={(e) => setBaudChoice(Number(e.target.value))}>
                {BAUD_RATES.map((b) => (
                  <option key={b} value={b}>
                    {b}
                  </option>
                ))}
              </select>
            </label>
          )}
          <p className="note">{selectedProfile?.note}</p>
          <button className="primary" onClick={connect} disabled={connecting || !profile}>
            {connecting ? 'Connecting…' : `Connect to ${device.name}`}
          </button>
        </div>
      )}
    </section>
  )
}
