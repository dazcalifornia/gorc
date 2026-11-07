// Mirrors the JSON emitted by the Go backend (internal/manager, internal/transport).

export type TransportKind = 'sim' | 'serial' | 'ble' | 'bt-classic'

export type Quality = 'ok' | 'searching' | 'artifact' | 'no_sensor'

export interface Availability {
  kind: TransportKind
  label: string
  available: boolean
  note: string
}

export interface Profile {
  id: string
  label: string
  note: string
  transports: TransportKind[]
  defaultBaud?: number
}

export interface DeviceInfo {
  id: string
  kind: TransportKind
  name: string
  address: string
  rssi?: number
  details?: Record<string, string>
  services?: string[]
  suggestedProfile?: string
}

export interface Reading {
  sessionId: string
  deviceId: string
  metric: string
  value: number
  unit: string
  quality: Quality
  at: string
  waveform?: boolean
}

export type SessionState = 'connecting' | 'live' | 'closed' | 'error'

export interface Session {
  id: string
  device: DeviceInfo
  profile: string
  baudRate?: number
  state: SessionState
  error?: string
  startedAt: string
  endedAt?: string
  frames: number
  bytes: number
  readingCount: number
  latest: Reading[]
}

export interface LogLine {
  sessionId: string
  at: string
  level: 'info' | 'warn' | 'error' | 'rx' | 'tx'
  message: string
  hex?: string
}

export interface Event {
  type: 'snapshot' | 'session' | 'readings' | 'log'
  at: string
  sessions?: Session[]
  session?: Session
  readings?: Reading[]
  log?: LogLine
}

export interface OpenRequest {
  transport: TransportKind
  deviceId: string
  address?: string
  profile: string
  baudRate?: number
}
