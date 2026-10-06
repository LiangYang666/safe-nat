// Typed client for the safe-nat management API (design.md §7).
// All requests are same-origin; the session rides in an HttpOnly cookie.

export interface Stats {
  uptime_sec: number
  clients: number
  tunnels: number
  conn_active: number
  conn_total: number
  blocked_total: number
  whitelist_rules: number
}

export interface TunnelView {
  name: string
  type: string // tcp | socks5
  remote_port: number
  firewall: boolean
  tls: boolean
  client: string
  conn_active: number
  conn_total: number
  blocked: number
}

export interface SessionView {
  client: string
  addr: string
  connected: string
  tunnels: number
  conns: number
}

export interface WhitelistRule {
  id: number
  rule: string
  region?: string
  created_at: string
}

export interface MyIpInfo {
  ip: string
  region?: string
  covered?: boolean // already matched by an existing rule (exact or CIDR)
  rule?: string // the stored rule covering ip, when covered
}

// One aggregated refusal: a single (ip, tunnel, port, kind) with counters.
export interface BlockedRow {
  ip: string
  tunnel: string
  remote_port: number
  kind: string // blocked | tls_fail
  count: number
  first_seen: string
  last_seen: string
  last_reason?: string
  region?: string
  covered: boolean // a whitelist rule already covers this IP
  rule?: string
}

export interface BlockedHit {
  time: string
  ip: string
  tunnel: string
  remote_port: number
  kind: string
  reason?: string
}

export interface BlockedSummary {
  total: number // lifetime refusals
  recent: number // raw hits inside the retention window
  unique_ips: number
  rows: number
  dropped: number // hits lost to a full queue (data plane first)
}

export interface BlockedLog {
  summary: BlockedSummary
  rows: BlockedRow[]
  recent: BlockedHit[]
}

export interface TrafficLive {
  tunnel: string
  up_bps: number
  down_bps: number
  up_total: number
  down_total: number
}

export interface TrafficDaily {
  tunnel: string
  day: string
  up_bytes: number
  down_bytes: number
}

export interface TrafficSeriesRow {
  ts: string // "YYYY-MM-DD HH:MM" or hour "YYYY-MM-DD HH:00"
  up_bytes: number
  down_bytes: number
}

export interface ServerEvent {
  type: string
  time: string
  client?: string
  client_ip?: string
  tunnel?: string
  tunnel_type?: string
  remote_port?: number
  ip?: string
  conn_id?: number
  reason?: string
  detail?: string
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (res.status === 401) {
    throw new ApiError(401, '未登录或会话已过期')
  }
  const text = await res.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    /* non-JSON body */
  }
  if (!res.ok) {
    throw new ApiError(res.status, data?.error ?? `请求失败 (${res.status})`)
  }
  return data as T
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export const api = {
  login: (username: string, password: string) =>
    request<{ ok: boolean; username: string }>('/api/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request<{ ok: boolean }>('/api/logout', { method: 'POST' }),
  session: () => request<{ ok: boolean; username: string }>('/api/session'),
  stats: () => request<Stats>('/api/stats'),
  tunnels: () => request<TunnelView[]>('/api/tunnels'),
  sessions: () => request<SessionView[]>('/api/sessions'),
  whitelist: () => request<WhitelistRule[]>('/api/whitelist'),
  whitelistMe: () => request<MyIpInfo>('/api/whitelist/me'),
  trafficLive: () => request<TrafficLive[]>('/api/traffic/live'),
  trafficDaily: (days = 7, tunnel = '') =>
    request<TrafficDaily[]>(
      `/api/traffic/daily?days=${days}${tunnel ? `&tunnel=${encodeURIComponent(tunnel)}` : ''}`,
    ),
  trafficSeries: (days = 1, tunnel = '', bucket: 'm' | 'h' = 'm') =>
    request<TrafficSeriesRow[]>(
      `/api/traffic/series?days=${days}&bucket=${bucket}${tunnel ? `&tunnel=${encodeURIComponent(tunnel)}` : ''}`,
    ),
  whitelistAdd: (rule: string) =>
    request<WhitelistRule>('/api/whitelist', {
      method: 'POST',
      body: JSON.stringify({ rule }),
    }),
  whitelistDelete: (id: number) =>
    request<{ ok: boolean }>(`/api/whitelist/${id}`, { method: 'DELETE' }),
  blocked: (limit = 200, recent = 50) =>
    request<BlockedLog>(`/api/blocked?limit=${limit}&recent=${recent}`),
  blockedClear: () => request<{ ok: boolean }>('/api/blocked', { method: 'DELETE' }),
}

export function fmtUptime(sec: number): string {
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m}m ${sec % 60}s`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  const d = Math.floor(h / 24)
  return `${d}d ${h % 24}h`
}

export function fmtTime(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtFullTime(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
