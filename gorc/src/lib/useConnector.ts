import { useSyncExternalStore } from 'react'
import type { Event, LogLine, Reading, Session } from './types'

/** Waveform samples kept per session — about ten seconds at 60 Hz. */
const WAVE_CAPACITY = 600
/** Log lines retained in memory. */
const LOG_CAPACITY = 250
/**
 * How often React re-renders from the live data.
 *
 * A pulse oximeter produces 60 samples a second and the backend flushes 20
 * batches a second. Publishing a new snapshot per message would re-render the
 * whole console dozens of times a second for no visible benefit, so the store
 * coalesces changes and notifies at this rate instead.
 */
const RENDER_HZ = 8
/** Delay before retrying a dropped websocket — the backend may be restarting. */
const RECONNECT_MS = 1500

/** A fixed-size ring of waveform samples, drawn directly by the canvas. */
export interface Waveform {
  data: Float32Array
  /** Index of the next write position. */
  head: number
  /** Samples written so far, capped at capacity. */
  count: number
}

export type Status = 'connecting' | 'open' | 'closed'

/** The immutable view React renders. A new object is published per notify. */
export interface Snapshot {
  status: Status
  sessions: Session[]
  logs: LogLine[]
  /** Latest scalar reading per metric, by session id. */
  latest: Map<string, Map<string, Reading>>
  /** Session ids that have produced waveform samples. */
  waveformSessions: ReadonlySet<string>
}

/**
 * ConnectorStore owns the websocket and all device state.
 *
 * It is an external store rather than a pile of hooks for two reasons: device
 * data arrives far faster than React should re-render, and the waveform must
 * be mutated in place so the canvas can read it without allocating. Keeping
 * both outside React and publishing coalesced snapshots is what makes a 60 Hz
 * waveform cost nothing.
 */
class ConnectorStore {
  private listeners = new Set<() => void>()
  private socket: WebSocket | null = null
  private retry: ReturnType<typeof setTimeout> | undefined
  private pulse: ReturnType<typeof setInterval> | undefined
  private stopped = false

  private status: Status = 'connecting'
  private sessions = new Map<string, Session>()
  private latest = new Map<string, Map<string, Reading>>()
  private logs: LogLine[] = []
  private waves = new Map<string, Waveform>()
  private dirty = false

  private snapshot: Snapshot = {
    status: 'connecting',
    sessions: [],
    logs: [],
    latest: new Map(),
    waveformSessions: new Set(),
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    if (this.listeners.size === 1) this.start()
    return () => {
      this.listeners.delete(listener)
      if (this.listeners.size === 0) this.stop()
    }
  }

  getSnapshot = (): Snapshot => this.snapshot

  /**
   * waveform returns the live ring buffer for a session. The canvas calls this
   * every animation frame; the buffer is mutated in place, so the object
   * identity never changes and no snapshot is involved.
   */
  waveform = (sessionId: string): Waveform | undefined => this.waves.get(sessionId)

  clearLogs = (): void => {
    this.logs = []
    this.publish()
  }

  private start() {
    this.stopped = false
    this.connect()
    // Coalesce: republish only when something arrived since the last tick.
    this.pulse = setInterval(() => {
      if (!this.dirty) return
      this.dirty = false
      this.publish()
    }, 1000 / RENDER_HZ)
  }

  private stop() {
    this.stopped = true
    if (this.retry) clearTimeout(this.retry)
    if (this.pulse) clearInterval(this.pulse)
    this.socket?.close()
    this.socket = null
  }

  private connect() {
    if (this.stopped) return
    this.setStatus('connecting')

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${proto}//${location.host}/ws`)
    this.socket = socket

    socket.onopen = () => this.setStatus('open')
    socket.onmessage = (msg) => this.handle(JSON.parse(msg.data as string) as Event)
    socket.onerror = () => socket.close()
    socket.onclose = () => {
      if (this.socket !== socket) return
      this.setStatus('closed')
      if (!this.stopped) this.retry = setTimeout(() => this.connect(), RECONNECT_MS)
    }
  }

  private handle(ev: Event) {
    switch (ev.type) {
      case 'snapshot':
        this.sessions = new Map((ev.sessions ?? []).map((s) => [s.id, s]))
        for (const s of ev.sessions ?? []) this.applyReadings(s.latest ?? [])
        break
      case 'session':
        if (ev.session) this.sessions.set(ev.session.id, ev.session)
        break
      case 'readings':
        this.applyReadings(ev.readings ?? [])
        break
      case 'log':
        if (ev.log) this.logs = [ev.log, ...this.logs].slice(0, LOG_CAPACITY)
        break
    }
    this.dirty = true
  }

  private applyReadings(readings: Reading[]) {
    for (const r of readings) {
      if (r.waveform) {
        let w = this.waves.get(r.sessionId)
        if (!w) {
          w = { data: new Float32Array(WAVE_CAPACITY), head: 0, count: 0 }
          this.waves.set(r.sessionId, w)
        }
        w.data[w.head] = r.value
        w.head = (w.head + 1) % w.data.length
        if (w.count < w.data.length) w.count++
        continue
      }
      let metrics = this.latest.get(r.sessionId)
      if (!metrics) {
        metrics = new Map()
        this.latest.set(r.sessionId, metrics)
      }
      metrics.set(r.metric, r)
    }
  }

  private setStatus(status: Status) {
    if (this.status === status) return
    this.status = status
    this.publish()
  }

  /** Builds a fresh immutable snapshot and notifies React. */
  private publish() {
    this.snapshot = {
      status: this.status,
      // Newest session first, matching the backend's ordering.
      sessions: [...this.sessions.values()].sort((a, b) =>
        b.startedAt.localeCompare(a.startedAt),
      ),
      logs: this.logs,
      latest: new Map([...this.latest].map(([id, m]) => [id, new Map(m)])),
      waveformSessions: new Set(this.waves.keys()),
    }
    for (const listener of this.listeners) listener()
  }
}

const store = new ConnectorStore()

export interface Connector extends Snapshot {
  waveform: (sessionId: string) => Waveform | undefined
  clearLogs: () => void
}

/** Subscribes a component to the live device stream. */
export function useConnector(): Connector {
  const snapshot = useSyncExternalStore(store.subscribe, store.getSnapshot)
  return { ...snapshot, waveform: store.waveform, clearLogs: store.clearLogs }
}
