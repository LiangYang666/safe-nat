<script setup lang="ts">
import { computed } from 'vue'
import { store, fmtUp } from '../store'

const cards = computed(() => [
  { label: '在线客户端', value: String(store.stats.clients), hint: '已连接会话', cls: 'text-sky-300' },
  { label: '隧道', value: String(store.stats.tunnels), hint: `socks5 也在其中`, cls: 'text-emerald-300' },
  { label: '当前连接', value: String(store.stats.conn_active), hint: `累计 ${store.stats.conn_total}`, cls: 'text-slate-200' },
  { label: '白名单规则', value: String(store.stats.whitelist_rules), hint: '精确 IP + CIDR', cls: 'text-amber-300' },
  { label: '累计拦截', value: String(store.stats.blocked_total), hint: `独立来源 IP ${store.blocked.summary.unique_ips}`, cls: 'text-rose-400' },
  { label: '运行时长', value: fmtUp(store.stats.uptime_sec), hint: 'server 自启动起', cls: 'text-slate-300' },
])

const liveConns = computed(() => store.tunnels.reduce((n, t) => n + t.conn_active, 0))
</script>

<template>
  <div class="mx-auto max-w-6xl space-y-6">
    <!-- stat cards -->
    <div class="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
      <div
        v-for="c in cards"
        :key="c.label"
        class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4 transition-colors hover:border-[#243044]"
      >
        <div class="text-[11px] tracking-wide text-slate-500">{{ c.label }}</div>
        <div class="mono mt-2 text-2xl font-semibold" :class="c.cls">{{ c.value }}</div>
        <div class="mt-1 text-[11px] text-slate-600">{{ c.hint }}</div>
      </div>
    </div>

    <!-- live activity strip -->
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] px-5 py-4">
      <div class="flex items-center gap-2 text-xs text-slate-500">
        <span
          class="inline-block h-2 w-2 rounded-full"
          :class="store.live ? 'bg-emerald-400 shadow-[0_0_8px] shadow-emerald-400/70' : 'bg-slate-600'"
        ></span>
        <span class="font-medium text-slate-400">实时事件流</span>
        <span class="ml-auto mono">{{ store.events.length }} 条记录 · 当前连接 {{ liveConns }}</span>
      </div>
      <div v-if="store.events.length" class="mt-3 flex items-center gap-2 overflow-hidden">
        <div class="flex min-w-0 items-center gap-2">
          <span class="shrink-0 rounded bg-[#182130] px-1.5 py-0.5 text-[10px] uppercase text-slate-500">{{ store.events[0].kind }}</span>
          <span class="mono shrink-0 text-[11px] text-slate-600">{{ store.events[0].time }}</span>
          <span class="truncate text-[13px]" :class="store.events[0].cls">{{ store.events[0].text }}</span>
        </div>
        <span class="ml-auto shrink-0 text-[11px] text-slate-600">最近事件</span>
      </div>
      <div v-else class="mt-3 text-[13px] text-slate-600">暂无事件 —— 连接穿透端口或触发白名单拦截后，这里会实时出现记录。</div>
    </div>

    <div class="grid gap-6 lg:grid-cols-2">
      <!-- connected clients -->
      <section class="rounded-xl border border-[#1a2230] bg-[#0d131c]">
        <header class="border-b border-[#1a2230] px-5 py-3 text-[13px] font-medium text-slate-300">在线客户端</header>
        <div v-if="store.sessions.length" class="divide-y divide-[#151d29]">
          <div v-for="s in store.sessions" :key="s.addr" class="flex items-center gap-3 px-5 py-3">
            <span class="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-sky-500/10 text-sky-400">
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor"><path d="M3 5h18v14H3V5zm2 2v10h14V7H5zm4 2h6v1H9V9zm0 2.5h6v1H9v-1zm0 2.5h4v1H9v-1z"/></svg>
            </span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="truncate text-[13px] text-slate-200">{{ s.client }}</span>
                <span class="rounded bg-emerald-500/10 px-1.5 py-px text-[10px] text-emerald-400">在线</span>
              </div>
              <div class="mono mt-0.5 truncate text-[11px] text-slate-600">{{ s.addr }}</div>
            </div>
            <div class="text-right">
              <div class="mono text-sm text-slate-300">{{ s.tunnels }}</div>
              <div class="text-[10px] text-slate-600">隧道 · {{ s.conns }} 连接</div>
            </div>
          </div>
        </div>
        <div v-else class="px-5 py-8 text-center text-sm text-slate-600">当前没有在线的客户端</div>
      </section>

      <!-- per-tunnel counters -->
      <section class="rounded-xl border border-[#1a2230] bg-[#0d131c]">
        <header class="border-b border-[#1a2230] px-5 py-3 text-[13px] font-medium text-slate-300">隧道计数</header>
        <div v-if="store.tunnels.length" class="divide-y divide-[#151d29]">
          <div v-for="t in store.tunnels" :key="t.remote_port + t.client" class="flex items-center gap-3 px-5 py-2.5">
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px] uppercase tracking-wide"
              :class="t.type === 'socks5' ? 'bg-violet-500/10 text-violet-300' : 'bg-sky-500/10 text-sky-300'"
            >{{ t.type }}</span>
            <div class="min-w-0 flex-1">
              <div class="truncate text-[13px] text-slate-200">{{ t.name }}</div>
              <div class="mono text-[11px] text-slate-600">:{{ t.remote_port }} · {{ t.client }}</div>
            </div>
            <div class="mono flex items-center gap-4 text-[12px]">
              <span class="text-emerald-400">{{ t.conn_active }}<span class="text-slate-600">/{{ t.conn_total }}</span></span>
              <span v-if="t.blocked" class="w-10 text-right text-rose-400">{{ t.blocked }}</span>
            </div>
          </div>
        </div>
        <div v-else class="px-5 py-8 text-center text-sm text-slate-600">暂无隧道</div>
      </section>
    </div>
  </div>
</template>
