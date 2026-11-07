import { useMemo, useState } from 'react'
import type { LogLine } from '../lib/types'

interface Props {
  logs: LogLine[]
  onClear: () => void
}

/**
 * FrameLog shows raw traffic and session events. It is the view you need when
 * a device is connected but silent, or speaking a protocol you have not
 * identified yet — so the hex is shown verbatim.
 */
export function FrameLog({ logs, onClear }: Props) {
  const [showRaw, setShowRaw] = useState(true)

  const visible = useMemo(
    () => (showRaw ? logs : logs.filter((l) => l.level !== 'rx')),
    [logs, showRaw],
  )

  return (
    <section className="panel log-panel">
      <h2>
        Traffic
        <span className="header-actions">
          <label className="checkbox">
            <input
              type="checkbox"
              checked={showRaw}
              onChange={(e) => setShowRaw(e.target.checked)}
            />
            raw frames
          </label>
          <button onClick={onClear}>Clear</button>
        </span>
      </h2>

      {visible.length === 0 ? (
        <p className="muted">Nothing yet. Connect a device to see its traffic.</p>
      ) : (
        <ol className="log">
          {visible.map((l, i) => (
            <li key={`${l.at}-${i}`} className={`log-${l.level}`}>
              <span className="log-time">{new Date(l.at).toLocaleTimeString()}</span>
              <span className="log-level">{l.level}</span>
              <span className="log-session">{l.sessionId}</span>
              <span className="log-message">{l.message}</span>
              {l.hex && <code className="log-hex">{formatHex(l.hex)}</code>}
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}

/** Groups hex into byte pairs so a frame boundary is visible at a glance. */
function formatHex(hex: string): string {
  return hex.match(/.{1,2}/g)?.join(' ') ?? hex
}
