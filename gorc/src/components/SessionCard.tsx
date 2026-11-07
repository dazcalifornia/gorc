import { useState } from 'react'
import { api } from '../lib/api'
import { QUALITY_LABEL, formatValue, metricAccent, metricLabel, sortMetrics } from '../lib/metrics'
import type { Reading, Session } from '../lib/types'
import type { Waveform as WaveBuffer } from '../lib/useConnector'
import { Waveform } from './Waveform'

interface Props {
  session: Session
  latest: Map<string, Reading> | undefined
  readWaveform: () => WaveBuffer | undefined
  hasWaveform: boolean
}

export function SessionCard({ session, latest, readWaveform, hasWaveform }: Props) {
  const [command, setCommand] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  // Prefer the fast reading stream; fall back to the session snapshot so a
  // page reload shows values before the next measurement arrives.
  const readings = latest && latest.size > 0 ? [...latest.values()] : session.latest
  const tiles = sortMetrics(readings)
  const live = session.state === 'live'

  const send = async () => {
    if (!command.trim()) return
    setBusy(true)
    setError(undefined)
    try {
      // Anything that is pure hex is sent as bytes; everything else as text.
      const hexish = /^[0-9a-fA-F\s:-]+$/.test(command) && command.replace(/[\s:-]/g, '').length % 2 === 0
      await api.write(session.id, hexish ? { hex: command } : { text: command })
      setCommand('')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const close = async () => {
    setBusy(true)
    try {
      await api.close(session.id)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <article className={`card state-${session.state}`}>
      <header>
        <div>
          <h3>{session.device.name}</h3>
          <p className="muted">
            {session.device.kind} · {session.profile}
            {session.baudRate ? ` · ${session.baudRate} baud` : ''} · {session.id}
          </p>
        </div>
        <div className="card-actions">
          <span className={`pill ${session.state}`}>{session.state}</span>
          {live && (
            <button onClick={close} disabled={busy}>
              Disconnect
            </button>
          )}
        </div>
      </header>

      {session.error && <p className="error">{session.error}</p>}

      {tiles.length === 0 ? (
        <p className="muted waiting">
          {live ? 'Connected — waiting for the first measurement…' : 'No measurements recorded.'}
        </p>
      ) : (
        <div className="tiles">
          {tiles.map((r) => (
            <div key={r.metric} className={`tile accent-${metricAccent(r.metric)}`}>
              <span className="tile-label">{metricLabel(r.metric)}</span>
              <span className="tile-value">
                {formatValue(r)}
                <small>{r.unit}</small>
              </span>
              {QUALITY_LABEL[r.quality] && (
                <span className={`tile-quality ${r.quality}`}>{QUALITY_LABEL[r.quality]}</span>
              )}
            </div>
          ))}
        </div>
      )}

      {hasWaveform && <Waveform read={readWaveform} label="Plethysmogram" />}

      <dl className="counters">
        <div>
          <dt>Frames</dt>
          <dd>{session.frames.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Bytes</dt>
          <dd>{session.bytes.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Readings</dt>
          <dd>{session.readingCount.toLocaleString()}</dd>
        </div>
      </dl>

      {live && (
        <div className="row send">
          <input
            value={command}
            placeholder="Send to device: hex (7D 81 A1) or text"
            onChange={(e) => setCommand(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void send()
            }}
          />
          <button onClick={send} disabled={busy || !command.trim()}>
            Send
          </button>
        </div>
      )}

      {error && <p className="error">{error}</p>}
    </article>
  )
}
