<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type TrafficDaily, type TrafficLive, type TrafficSeriesRow } from '../api'

const DAYS = 7
const live = ref<TrafficLive[]>([])
const daily = ref<TrafficDaily[]>([])
const series = ref<TrafficSeriesRow[]>([])
const tunnelFilter = ref('') // '' = all
const liveOn = ref(true)
const now = ref(Date.now())
const rangeDays = ref(1) // curve window: 1 (default) or 7

let liveTimer = 0
let seriesTimer = 0
let clockTimer = 0

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${u[i]}`
}
function fmtRate(bps: number): string {
  return `${fmtBytes(bps)}/s`
}

const everyTunnel = computed(() => {
  const s = new Set<string>()
  live.value.forEach((l) => s.add(l.tunnel))
  daily.value.forEach((d) => s.add(d.tunnel))
  return [...s].sort()
})

const dayAgg = computed(() => {
  const rows = tunnelFilter.value
    ? daily.value.filter((d) => d.tunnel === tunnelFilter.value)
    : daily.value
  const map = new Map<string, { up: number; down: number }>()
  for (const r of rows) {
    const e = map.get(r.day) ?? { up: 0, down: 0 }
    e.up += r.up_bytes
    e.down += r.down_bytes
    map.set(r.day, e)
  }
  const days: { day: string; up: number; down: number }[] = []
  for (let i = DAYS - 1; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400_000)
    const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    days.push({ day: key, up: 0, down: 0, ...map.get(key) })
  }
  return days
})
const dayMax = computed(() => Math.max(1, ...dayAgg.value.map((d) => d.up + d.down)))
const dayHover = ref(-1)
const dayTip = computed(() => (dayHover.value >= 0 ? dayAgg.value[dayHover.value] ?? null : null))
function dayTipLeft(i: number): string {
  const n = dayAgg.value.length || 1
  const pct = Math.min(88, Math.max(12, ((i + 0.5) * 100) / n))
  return `${pct}%`
}

const liveFiltered = computed(() =>
  tunnelFilter.value ? live.value.filter((l) => l.tunnel === tunnelFilter.value) : live.value,
)

// ---- curve chart ----
function parseTs(ts: string): number {
  // "YYYY-MM-DD HH:MM"
  const m = ts.match(/^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2})/)
  if (!m) return 0
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]).getTime()
}

const W = 600
const H = 150
const curve = computed(() => {
  const rows = series.value
  if (!rows.length) return null
  const t0 = parseTs(rows[0].ts)
  const t1 = parseTs(rows[rows.length - 1].ts)
  const span = Math.max(1, t1 - t0)
  let maxV = 1
  let peak = { ts: rows[0].ts, v: 0 }
  let upTot = 0
  let downTot = 0
  const pts = rows.map((r) => {
    const x = ((parseTs(r.ts) - t0) / span) * W
    upTot += r.up_bytes
    downTot += r.down_bytes
    const v = Math.max(r.up_bytes, r.down_bytes)
    if (v > maxV) maxV = v
    if (r.up_bytes + r.down_bytes > peak.v) peak = { ts: r.ts, v: r.up_bytes + r.down_bytes }
    return { x, up: r.up_bytes, down: r.down_bytes, ts: r.ts }
  })
  const yOf = (v: number) => H - 6 - (v / maxV) * (H - 16)
  const line = (sel: 'up' | 'down') =>
    pts
      .map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${yOf(p[sel]).toFixed(1)}`)
      .join(' ')
  const labelT = (frac: number) => {
    const t = t0 + span * frac
    const d = new Date(t)
    const p = (n: number) => String(n).padStart(2, '0')
    const hhmm = `${p(d.getHours())}:${p(d.getMinutes())}`
    return rangeDays.value > 1 ? `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hhmm}` : hhmm
  }
  return { pts, line, maxV, yOf, labelT, upTot, downTot, peak }
})
const seriesEmpty = computed(() => !series.value.length)

// ---- hover / tooltip ----
const plotRef = ref<HTMLElement | null>(null)
const svgRef = ref<SVGSVGElement | null>(null)
const hoverIdx = ref(-1)
const tooltipX = ref(0)

function fmtAxisTs(ts: string): string {
  const d = new Date(parseTs(ts))
  const p = (n: number) => String(n).padStart(2, '0')
  const hhmm = `${p(d.getHours())}:${p(d.getMinutes())}`
  return rangeDays.value > 1 ? `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hhmm}` : hhmm
}
const hov = computed(() => {
  const c = curve.value
  if (!c || hoverIdx.value < 0) return null
  const p = c.pts[hoverIdx.value]
  if (!p) return null
  return { p, x: p.x, yUp: c.yOf(p.up), yDown: c.yOf(p.down), label: fmtAxisTs(p.ts) }
})
function onPointer(e: PointerEvent) {
  const c = curve.value
  const svg = svgRef.value
  const plot = plotRef.value
  if (!c || !svg || !plot) return
  const sr = svg.getBoundingClientRect()
  if (sr.width <= 0) return
  // viewBox x under cursor, snap to nearest data point
  const vx = ((e.clientX - sr.left) / sr.width) * W
  let best = 0
  let bd = Infinity
  for (let i = 0; i < c.pts.length; i++) {
    const d = Math.abs(c.pts[i].x - vx)
    if (d < bd) {
      bd = d
      best = i
    }
  }
  hoverIdx.value = best
  // tooltip left (px, inside plot), clamped so it doesn't overflow
  const pr = plot.getBoundingClientRect()
  tooltipX.value = Math.min(Math.max(e.clientX - pr.left, 80), Math.max(80, pr.width - 80))
}
function clearHover() {
  hoverIdx.value = -1
}

async function refreshSeries() {
  try {
    series.value = await api.trafficSeries(rangeDays.value, tunnelFilter.value, rangeDays.value > 1 ? 'h' : 'm')
    now.value = Date.now()
  } catch {
    /* retry on next tick / filter change */
  }
}

async function refreshLive() {
  if (!liveOn.value) return
  try {
    live.value = await api.trafficLive()
    now.value = Date.now()
  } catch {
    /* next tick retries */
  }
}
async function refreshDaily() {
  try {
    daily.value = await api.trafficDaily(DAYS, tunnelFilter.value)
  } catch {
    /* retry on filter change */
  }
}

function pick(t: string) {
  tunnelFilter.value = t
  refreshDaily()
  refreshSeries()
}
function pickRange(d: number) {
  rangeDays.value = d
  refreshSeries()
}

onMounted(() => {
  refreshLive()
  refreshDaily()
  refreshSeries()
  liveTimer = window.setInterval(refreshLive, 2000)
  seriesTimer = window.setInterval(refreshSeries, 60000)
  clockTimer = window.setInterval(() => (now.value = Date.now()), 2000)
})
onUnmounted(() => {
  window.clearInterval(liveTimer)
  window.clearInterval(seriesTimer)
  window.clearInterval(clockTimer)
})
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-4 md:space-y-5">
    <!-- filter row -->
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        class="rounded-lg px-2.5 py-1.5 text-[12px] transition-colors"
        :class="tunnelFilter === '' ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pick('')"
      >全部</button>
      <button
        v-for="t in everyTunnel"
        :key="t"
        class="mono rounded-lg px-2.5 py-1.5 text-[12px] transition-colors"
        :class="tunnelFilter === t ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pick(t)"
      >{{ t }}</button>
      <span class="mx-2 h-4 w-px bg-[#1f2937]"></span>
      <button
        class="rounded-lg px-2.5 py-1.5 text-[12px] transition-colors"
        :class="rangeDays === 1 ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pickRange(1)"
      >近 1 天</button>
      <button
        class="rounded-lg px-2.5 py-1.5 text-[12px] transition-colors"
        :class="rangeDays === 7 ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pickRange(7)"
      >近 7 天</button>
      <label class="ml-auto flex items-center gap-1.5 text-[11px] text-slate-500">
        <input v-model="liveOn" type="checkbox" class="accent-emerald-500" @change="refreshLive" />
        实时刷新
      </label>
    </div>

    <!-- curve -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex flex-wrap items-center justify-between gap-2 border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">流量曲线</span>
        <div class="flex items-center gap-3 text-[11px] text-slate-500">
          <span class="flex items-center gap-1"><i class="inline-block h-0.5 w-3 rounded bg-sky-400"></i>下载</span>
          <span class="flex items-center gap-1"><i class="inline-block h-0.5 w-3 rounded bg-violet-400"></i>上传</span>
          <span v-if="curve" class="text-slate-600">峰值 {{ fmtBytes(curve.peak.v) }}/{{ rangeDays > 1 ? '时' : '分' }}</span>
        </div>
      </div>
      <div class="px-2 py-4 md:px-4">
        <div v-if="curve" class="flex items-end gap-2">
          <div class="flex flex-col items-end justify-between pb-5 text-[10px] text-slate-600" style="height: 160px">
            <span>{{ fmtBytes(curve.maxV) }}</span>
            <span>0</span>
          </div>
          <div ref="plotRef" class="relative min-w-0 flex-1 select-none">
            <svg
              ref="svgRef"
              :viewBox="`0 0 ${W} ${H}`"
              preserveAspectRatio="none"
              class="h-36 w-full cursor-crosshair md:h-40"
              @pointermove="onPointer"
              @pointerdown="onPointer"
              @pointerleave="clearHover"
              @pointercancel="clearHover"
            >
              <!-- gridlines -->
              <line
                v-for="i in 3"
                :key="i"
                :x1="0"
                :x2="W"
                :y1="(H / 4) * i"
                :y2="(H / 4) * i"
                class="stroke-[#1a2230]"
                stroke-width="1"
              />
              <!-- hover crosshair -->
              <line
                v-if="hov"
                :x1="hov.x"
                :x2="hov.x"
                y1="0"
                :y2="H"
                class="stroke-[#334155]"
                stroke-width="1"
                stroke-dasharray="3 3"
              />
              <path :d="curve.line('down')" fill="none" stroke="#38bdf8" stroke-width="1.6" vector-effect="non-scaling-stroke" />
              <path :d="curve.line('up')" fill="none" stroke="#a78bfa" stroke-width="1.6" vector-effect="non-scaling-stroke" />
              <!-- hover dots -->
              <circle
                v-if="hov"
                :cx="hov.x"
                :cy="hov.yDown"
                r="3"
                fill="#0ea5e9"
                stroke="#0d131c"
                stroke-width="1.5"
              />
              <circle
                v-if="hov"
                :cx="hov.x"
                :cy="hov.yUp"
                r="3"
                fill="#a78bfa"
                stroke="#0d131c"
                stroke-width="1.5"
              />
            </svg>
            <div class="flex justify-between pt-1 text-[10px] text-slate-600">
              <span>{{ curve.labelT(0) }}</span>
              <span>{{ curve.labelT(0.5) }}</span>
              <span>{{ curve.labelT(1) }}</span>
            </div>
            <!-- tooltip -->
            <div
              v-if="hov"
              class="pointer-events-none absolute top-1 z-10 -translate-x-1/2 rounded-lg border border-[#233044] bg-[#0d131c]/95 px-2.5 py-1.5 text-[11px] shadow-xl"
              :style="{ left: tooltipX + 'px' }"
            >
              <div class="mono mb-1 text-[10px] text-slate-400">{{ hov.label }}</div>
              <div class="flex items-center gap-2.5 whitespace-nowrap">
                <span class="flex items-center gap-1"><i class="h-0.5 w-3 rounded bg-sky-400"></i><span class="mono text-slate-200">{{ fmtBytes(hov.p.down) }}</span></span>
                <span class="flex items-center gap-1"><i class="h-0.5 w-3 rounded bg-violet-400"></i><span class="mono text-slate-200">{{ fmtBytes(hov.p.up) }}</span></span>
              </div>
            </div>
          </div>
        </div>
        <div v-else class="py-14 text-center text-sm text-slate-600">
          {{ seriesEmpty ? '该时段暂无流量数据 —— 有连接传输后曲线会出现' : '加载中…' }}
        </div>
      </div>
      <div v-if="curve" class="flex flex-wrap gap-x-5 gap-y-1 border-t border-[#1a2230] px-4 py-2.5 text-[11px] text-slate-500 md:px-5">
        <span>窗口下载 <b class="mono text-sky-400">{{ fmtBytes(curve.downTot) }}</b></span>
        <span>窗口上传 <b class="mono text-violet-400">{{ fmtBytes(curve.upTot) }}</b></span>
        <span>数据点 <b class="mono text-slate-400">{{ series.length }}</b></span>
        <span class="ml-auto text-slate-600">每 {{ rangeDays > 1 ? '小时' : '分钟' }}一个点 · 保留 7 天</span>
      </div>
    </div>

    <!-- live throughput -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">实时吞吐</span>
        <span class="mono text-[11px] text-slate-500">每 2s 采样 · {{ new Date(now).toLocaleTimeString() }}</span>
      </div>
      <table class="w-full text-left text-[13px]">
        <tbody class="divide-y divide-[#151d29]">
          <tr v-for="l in liveFiltered" :key="l.tunnel" class="transition-colors hover:bg-[#111827]/60">
            <td class="mono px-4 py-3 text-slate-200 md:px-5">{{ l.tunnel }}</td>
            <td class="px-3 py-3">
              <span class="mr-1.5 text-[10px] text-sky-500">↓</span>
              <span class="mono text-slate-300">{{ fmtRate(l.down_bps) }}</span>
              <span class="ml-3 mr-1.5 text-[10px] text-violet-500">↑</span>
              <span class="mono text-slate-300">{{ fmtRate(l.up_bps) }}</span>
            </td>
            <td class="px-3 py-3 text-right">
              <span class="text-[11px] text-slate-600">累计 </span>
              <span class="mono text-slate-400">{{ fmtBytes(l.down_total + l.up_total) }}</span>
            </td>
          </tr>
          <tr v-if="!liveFiltered.length">
            <td colspan="3" class="px-4 py-10 text-center text-sm text-slate-600">
              暂无流量 —— 隧道建立连接并传输数据后这里会出现速率
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- daily totals -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">每日总量</span>
        <span class="text-[11px] text-slate-500">（{{ tunnelFilter || '全部隧道' }}）</span>
      </div>
      <div class="px-4 py-4 md:px-5">
        <div v-if="dayAgg.some((d) => d.up + d.down > 0)" class="relative">
          <div class="flex items-end gap-1.5 md:gap-2" @pointerleave="dayHover = -1" @pointercancel="dayHover = -1">
            <div
              v-for="(d, i) in dayAgg"
              :key="d.day"
              class="flex min-w-0 flex-1 cursor-crosshair flex-col items-center gap-1.5"
              @pointermove="dayHover = i"
              @pointerdown="dayHover = i"
            >
              <div class="flex h-28 w-full items-end justify-center gap-px md:h-40">
                <div
                  class="w-1/3 max-w-[14px] rounded-t-sm bg-sky-500/70 transition-all"
                  :style="{ height: (d.down / dayMax) * 100 + '%' }"
                ></div>
                <div
                  class="w-1/3 max-w-[14px] rounded-t-sm bg-violet-500/70 transition-all"
                  :style="{ height: (d.up / dayMax) * 100 + '%' }"
                ></div>
              </div>
              <div
                class="mono text-[10px] transition-colors"
                :class="i === dayHover ? 'text-slate-200' : 'text-slate-500'"
              >{{ d.day.slice(5) }}</div>
              <div
                class="mono hidden text-[10px] transition-colors md:block"
                :class="i === dayHover ? 'text-slate-300' : 'text-slate-600'"
              >{{ fmtBytes(d.up + d.down) }}</div>
            </div>
          </div>
          <!-- tooltip -->
          <div
            v-if="dayTip"
            class="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-lg border border-[#233044] bg-[#0d131c]/95 px-2.5 py-1.5 text-[11px] shadow-xl"
            :style="{ left: dayTipLeft(dayHover) }"
          >
            <div class="mono mb-1 text-[10px] text-slate-400">{{ dayTip.day }}</div>
            <div class="flex flex-col gap-0.5 whitespace-nowrap">
              <span class="flex items-center gap-1"><i class="h-0.5 w-3 rounded bg-sky-400"></i><span class="text-slate-500">下载</span><span class="mono ml-auto text-slate-200">{{ fmtBytes(dayTip.down) }}</span></span>
              <span class="flex items-center gap-1"><i class="h-0.5 w-3 rounded bg-violet-400"></i><span class="text-slate-500">上传</span><span class="mono ml-auto text-slate-200">{{ fmtBytes(dayTip.up) }}</span></span>
              <span class="mt-0.5 border-t border-[#1a2230] pt-0.5 text-slate-500">合计 <span class="mono float-right pl-3 text-slate-300">{{ fmtBytes(dayTip.down + dayTip.up) }}</span></span>
            </div>
          </div>
        </div>
        <div v-else class="py-10 text-center text-sm text-slate-600">暂无流量记录</div>
      </div>
    </div>
  </div>
</template>
