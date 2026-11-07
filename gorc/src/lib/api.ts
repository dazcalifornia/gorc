import type {
  Availability,
  DeviceInfo,
  OpenRequest,
  Profile,
  Session,
  TransportKind,
} from './types'

/** Unwraps the backend's `{error: "..."}` body into a thrown Error. */
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { 'content-type': 'application/json' } : undefined,
  })
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) detail = body.error
    } catch {
      // Non-JSON error body; the status line is all we have.
    }
    throw new Error(detail)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  transports: () => request<Availability[]>('/api/transports'),
  profiles: () => request<Profile[]>('/api/profiles'),

  scan: (transport: TransportKind) =>
    request<{ transport: TransportKind; devices: DeviceInfo[] }>(
      `/api/scan?transport=${encodeURIComponent(transport)}`,
    ).then((r) => r.devices ?? []),

  open: (req: OpenRequest) =>
    request<Session>('/api/sessions', { method: 'POST', body: JSON.stringify(req) }),

  sessions: () => request<Session[]>('/api/sessions'),

  close: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),

  forget: () => request<{ removed: number }>('/api/sessions', { method: 'DELETE' }),

  write: (id: string, payload: { hex?: string; text?: string }) =>
    request<{ written: number }>(`/api/sessions/${id}/write`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
}
