// Shared reactive state: dashboard snapshots (polled) + live event log (SSE).
import { reactive } from 'vue'
import { api, ApiError, fmtFullTime, fmtUptime } from './api'
import type { ServerEvent, SessionView, Stats, TunnelView, WhitelistRule } from './api'

export interface EventRow {
  kind: string
  time: string
  text: string
  cls: string // tailwind text color per kind
}

const MAX_EVENTS = 300

const emptyStats = (): Stats => ({
  uptime_sec: 0,
  clients: 0,
  tunnels: 0,
  conn_active: 0,
  conn_total: 0,
  blocked_total: 0,
  whitelist_rules: 0,
})

const kindStyle: Record<string, string> = {
  blocked: 'text-rose-400',
  conn_open: 'text-emerald-400',
  conn_close: 'text-slate-400',
  client_up: 'text-sky-400',
  client_down: 'text-amber-400',
  auth_fail: 'text-orange-400',
  login_fail: 'text-orange-400',
}

export const store = reactive({
  ready: false, // boot finished (session probe answered)
  authed: false,
  username: '',
  live: false, // SSE connected
  stats: emptyStats(),
  tunnels: [] as TunnelView[],
  sessions: [] as SessionView[],
  rules: [] as WhitelistRule[],
  events: [] as EventRow[],
  err: '', // transient banner text
})

let sse: EventSource | null = null
let pollTimer: number | undefined
let sseRetry = 0

export function fmtUp(sec: number): string {
  return fmtUptime(sec)
}

function pushEvent(ev: ServerEvent) {
  const cls = kindStyle[ev.type] ?? 'text-slate-300'
  let text = ''
  switch (ev.type) {
    case 'client_up':
      text = `客户端上线 · ${ev.client} (${ev.client_ip}) · ${ev.detail ?? ''}`
      break
    case 'client_down':
      text = `客户端离线 · ${ev.client} (${ev.client_ip}) · ${ev.detail ?? ''}`
      break
    case 'conn_open':
      text = `连接建立 · ${ev.tunnel} (${ev.tunnel_type ?? 'tcp'} :${ev.remote_port}) ← ${ev.ip} · conn#${ev.conn_id}`
      break
    case 'conn_close':
      text = `连接关闭 · ${ev.tunnel} :${ev.remote_port} · conn#${ev.conn_id} · ${ev.reason ?? ''}`
      break
    case 'blocked':
      text = `防火墙拦截 · ${ev.tunnel} :${ev.remote_port} ← ${ev.ip} (白名单拒绝)`
      break
    case 'auth_fail':
      text = `客户端接入被拒 · ${ev.client_ip} · ${ev.reason ?? ''}${ev.detail ? ` · ${ev.detail}` : ''}`
      break
    case 'login_fail':
      text = `登录失败 · ${ev.client_ip} · ${ev.reason ?? ''}`
      break
    default:
      text = `${ev.type} · ${ev.detail ?? JSON.stringify(ev)}`
  }
  store.events.unshift({ kind: ev.type, time: fmtFullTime(ev.time), text, cls })
  if (store.events.length > MAX_EVENTS) store.events.length = MAX_EVENTS
}

function connectSSE() {
  sse?.close()
  sse = new EventSource('/api/events')
  sse.onopen = () => {
    store.live = true
    sseRetry = 0
  }
  const onMessage = (e: MessageEvent) => {
    try {
      pushEvent(JSON.parse(e.data) as ServerEvent)
    } catch {
      /* ignore malformed frame */
    }
  }
  for (const name of ['client_up', 'client_down', 'conn_open', 'conn_close', 'blocked', 'auth_fail', 'login_fail']) {
    sse.addEventListener(name, onMessage)
  }
  sse.onerror = () => {
    store.live = false
    // EventSource auto-reconnects; keep the badge honest.
    sseRetry++
  }
}

async function refresh() {
  try {
    const [stats, tunnels, sessions, rules] = await Promise.all([
      api.stats(),
      api.tunnels(),
      api.sessions(),
      api.whitelist(),
    ])
    store.stats = stats
    store.tunnels = tunnels
    store.sessions = sessions
    store.rules = rules
    store.err = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      deauth('会话已过期')
    } else if (store.authed) {
      store.err = e instanceof Error ? e.message : String(e)
    }
  }
}

function startPolling() {
  stopPolling()
  pollTimer = window.setInterval(refresh, 3000)
}

function stopPolling() {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
  pollTimer = undefined
}

function deauth(msg: string) {
  store.authed = false
  store.err = msg
  sse?.close()
  sse = null
  stopPolling()
}

export async function boot() {
  try {
    const s = await api.session()
    store.authed = true
    store.username = s.username
    connectSSE()
    await refresh()
    startPolling()
  } catch {
    store.authed = false
  }
  store.ready = true
}

export async function login(username: string, password: string) {
  const res = await api.login(username, password)
  store.authed = true
  store.username = res.username
  connectSSE()
  await refresh()
  startPolling()
}

export async function logout() {
  try {
    await api.logout()
  } finally {
    deauth('')
  }
}

export function clearEvents() {
  store.events = []
}

export { api }
