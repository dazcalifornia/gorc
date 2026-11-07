import { api } from './lib/api'
import { useConnector } from './lib/useConnector'
import { FrameLog } from './components/FrameLog'
import { SessionCard } from './components/SessionCard'
import { TransportPanel } from './components/TransportPanel'

/**
 * The console. The browser never touches hardware here: the Go backend owns
 * the radios and serial ports, pushes every reading over one websocket, and
 * this app is a view onto that stream.
 */
export default function App() {
  const { status, sessions, logs, latest, waveformSessions, waveform, clearLogs } = useConnector()

  const live = sessions.filter((s) => s.state === 'live')
  const finished = sessions.filter((s) => s.state !== 'live')

  return (
    <div className="app">
      <header className="app-header">
        <div>
          <h1>Device connector</h1>
          <p className="muted">
            BLE, classic Bluetooth and USB serial medical devices — React front end, Go Fiber
            backend.
          </p>
        </div>
        <span className={`pill ${status}`}>
          {status === 'open' ? 'stream connected' : status === 'connecting' ? 'connecting…' : 'stream offline'}
        </span>
      </header>

      <main>
        <TransportPanel />

        <section className="panel sessions">
          <h2>
            Sessions
            {finished.length > 0 && (
              <span className="header-actions">
                <button onClick={() => void api.forget()}>Clear finished</button>
              </span>
            )}
          </h2>

          {sessions.length === 0 ? (
            <p className="muted">
              No sessions. Scan a transport on the left and connect to a device — the
              simulator needs no hardware.
            </p>
          ) : (
            <div className="cards">
              {[...live, ...finished].map((s) => (
                <SessionCard
                  key={s.id}
                  session={s}
                  latest={latest.get(s.id)}
                  readWaveform={() => waveform(s.id)}
                  hasWaveform={waveformSessions.has(s.id)}
                />
              ))}
            </div>
          )}
        </section>

        <FrameLog logs={logs} onClear={clearLogs} />
      </main>
    </div>
  )
}
