<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type TrafficDaily, type TrafficLive } from '../api'

const DAYS = 7
const live = ref<TrafficLive[]>([])
const daily = ref<TrafficDaily[]>([])
const tunnelFilter = ref('') // '' = all
const liveOn = ref(true)
const now = ref(Date.now())

let liveTimer = 0
let clockTimer = 0

// fmt: bytes -> human, bps -> human rate
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

// every tunnel ever seen (live ∪ daily), for the filter dropdown
const tunnels = computed(() => {
  const s = new Set<string>()
  live.value.forEach((l) => s.add(l.tunnel))
  daily.value.forEach((d) => s.add(d.tunnel))
  return [...s].sort()
})

// daily aggregation honoring the filter ('' = sum across tunnels)
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

const dayMax = computed(() =>
  Math.max(1, ...dayAgg.value.map((d) => d.up + d.down)),
)

const liveFiltered = computed(() =>
  tunnelFilter.value ? live.value.filter((l) => l.tunnel === tunnelFilter.value) : live.value,
)

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
}

onMounted(() => {
  refreshLive()
  refreshDaily()
  liveTimer = window.setInterval(refreshLive, 2000)
  clockTimer = window.setInterval(() => (now.value = Date.now()), 2000)
})
onUnmounted(() => {
  window.clearInterval(liveTimer)
  window.clearInterval(clockTimer)
})
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <!-- filter row -->
    <div class="flex flex-wrap items-center gap-2">
      <button
        class="rounded-lg px-3 py-1.5 text-[12px] transition-colors"
        :class="tunnelFilter === '' ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pick('')"
      >全部隧道</button>
      <button
        v-for="t in tunnels"
        :key="t"
        class="mono rounded-lg px-3 py-1.5 text-[12px] transition-colors"
        :class="tunnelFilter === t ? 'bg-emerald-500/15 text-emerald-300' : 'border border-[#233044] text-slate-400 hover:text-slate-200'"
        @click="pick(t)"
      >{{ t }}</button>
      <label class="ml-auto flex items-center gap-1.5 text-[11px] text-slate-500">
        <input v-model="liveOn" type="checkbox" class="accent-emerald-500" @change="refreshLive" />
        实时刷新
      </label>
    </div>

    <!-- live throughput -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-5 py-3">
        <span class="text-[13px] font-medium text-slate-300">实时吞吐</span>
        <span class="mono text-[11px] text-slate-500">每 2s 采样 · {{ new Date(now).toLocaleTimeString() }}</span>
      </div>
      <table class="w-full text-left text-[13px]">
        <tbody class="divide-y divide-[#151d29]">
          <tr v-for="l in liveFiltered" :key="l.tunnel" class="transition-colors hover:bg-[#111827]/60">
            <td class="mono px-5 py-3 text-slate-200">{{ l.tunnel }}</td>
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
            <td colspan="3" class="px-5 py-10 text-center text-sm text-slate-600">
              暂无流量 —— 隧道建立连接并传输数据后这里会出现速率
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- daily history bars -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-5 py-3">
        <span class="text-[13px] font-medium text-slate-300">近 {{ DAYS }} 天流量</span>
        <div class="flex items-center gap-3 text-[11px] text-slate-500">
          <span class="flex items-center gap-1"><i class="inline-block h-2 w-2 rounded-sm bg-sky-500/70"></i>下载</span>
          <span class="flex items-center gap-1"><i class="inline-block h-2 w-2 rounded-sm bg-violet-500/70"></i>上传</span>
          <span class="text-slate-600">（{{ tunnelFilter || '全部隧道' }}）</span>
        </div>
      </div>
      <div class="px-5 py-5">
        <div v-if="dayAgg.some((d) => d.up + d.down > 0)" class="flex items-end gap-2">
          <div v-for="d in dayAgg" :key="d.day" class="flex min-w-0 flex-1 flex-col items-center gap-1.5">
            <div class="flex h-40 w-full items-end justify-center gap-px">
              <div
                class="w-1/3 max-w-[14px] rounded-t-sm bg-sky-500/70 transition-all"
                :style="{ height: (d.down / dayMax) * 100 + '%' }"
                :title="`${d.day} 下载 ${fmtBytes(d.down)}`"
              ></div>
              <div
                class="w-1/3 max-w-[14px] rounded-t-sm bg-violet-500/70 transition-all"
                :style="{ height: (d.up / dayMax) * 100 + '%' }"
                :title="`${d.day} 上传 ${fmtBytes(d.up)}`"
              ></div>
            </div>
            <div class="mono text-[10px] text-slate-500">{{ d.day.slice(5) }}</div>
            <div class="mono text-[10px] text-slate-600">{{ fmtBytes(d.up + d.down) }}</div>
          </div>
        </div>
        <div v-else class="py-10 text-center text-sm text-slate-600">
          该时段暂无流量记录
        </div>
      </div>
    </div>
  </div>
</template>
