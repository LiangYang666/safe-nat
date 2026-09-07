<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { boot, login, logout, store } from './store'
import { ApiError } from './api'
import Overview from './views/Overview.vue'
import Tunnels from './views/Tunnels.vue'
import Whitelist from './views/Whitelist.vue'
import EventsLog from './views/EventsLog.vue'
import Login from './views/Login.vue'

type ViewName = 'overview' | 'tunnels' | 'whitelist' | 'events'

const view = ref<ViewName>('overview')
const views: { id: ViewName; label: string; icon: string }[] = [
  { id: 'overview', label: '总览', icon: 'M3 3h18v18H3V3zm2 2v14h14V5H5zm3 3h8v2H8V8zm0 4h8v2H8v-2zm0 4h5v2H8v-2z' },
  { id: 'tunnels', label: '隧道', icon: 'M12 3l9 5-9 5-9-5 9-5zm-7 8.5L12 16l7-4.5V15l-7 4.5-7-4.5v-3.5z' },
  { id: 'whitelist', label: 'IP 白名单', icon: 'M12 2l8 4v6c0 5-3.5 9-8 10-4.5-1-8-5-8-10V6l8-4zm0 2.3L6 7.5V12c0 3.6 2.4 6.6 6 7.6 3.6-1 6-4 6-7.6V7.5l-6-3.2zM11 7h2v2h-2V7zm0 3.5h2V15h-2v-4.5z' },
  { id: 'events', label: '安全日志', icon: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 2a8 8 0 110 16 8 8 0 010-16zm-1 4h2v6h-2V8zm0 8h2v2h-2v-2z' },
]

const current = computed(() => views.find((v) => v.id === view.value))

async function onLogin(u: string, p: string): Promise<void> {
  try {
    await login(u, p)
    view.value = 'overview'
  } catch (e) {
    if (e instanceof ApiError) throw e
    throw new ApiError(0, e instanceof Error ? e.message : '登录失败')
  }
}

async function onLogout() {
  await logout()
}

onMounted(boot)
</script>

<template>
  <div v-if="!store.ready" class="h-full grid place-items-center">
    <div class="flex items-center gap-3 text-slate-500">
      <span class="inline-block h-5 w-5 animate-spin rounded-full border-2 border-slate-600 border-t-emerald-400"></span>
      <span class="text-sm">safe-nat 加载中…</span>
    </div>
  </div>

  <Login v-else-if="!store.authed" :err="store.err" @login="onLogin" />

  <div v-else class="flex h-full">
    <!-- Sidebar -->
    <aside class="flex w-56 shrink-0 flex-col border-r border-[#1a2230] bg-[#0b1017]">
      <div class="flex items-center gap-2.5 px-5 py-5">
        <div class="grid h-8 w-8 place-items-center rounded-lg bg-emerald-500/15 text-emerald-400">
          <svg class="h-5 w-5" viewBox="0 0 24 24" fill="currentColor">
            <path d="M12 2l9 5v5.5c0 5.2-3.7 9.3-9 10.5-5.3-1.2-9-5.3-9-10.5V7l9-5z" />
          </svg>
        </div>
        <div>
          <div class="text-[15px] font-semibold tracking-wide text-slate-100">safe-nat</div>
          <div class="text-[10px] uppercase tracking-widest text-slate-500">管理面板</div>
        </div>
      </div>
      <nav class="mt-2 flex-1 space-y-1 px-3">
        <button
          v-for="v in views"
          :key="v.id"
          class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-[13px] transition-colors"
          :class="view === v.id ? 'bg-emerald-500/10 text-emerald-300' : 'text-slate-400 hover:bg-[#141c28] hover:text-slate-200'"
          @click="view = v.id"
        >
          <svg class="h-4 w-4 shrink-0" viewBox="0 0 24 24" fill="currentColor"><path :d="v.icon" /></svg>
          {{ v.label }}
          <span
            v-if="v.id === 'whitelist' && store.rules.length"
            class="ml-auto rounded-full bg-[#1c2634] px-1.5 py-0.5 text-[10px] text-slate-400"
          >{{ store.rules.length }}</span>
        </button>
      </nav>
      <div class="border-t border-[#1a2230] px-5 py-3 text-[11px] text-slate-600">
        <div class="flex items-center gap-1.5">
          <span
            class="inline-block h-1.5 w-1.5 rounded-full"
            :class="store.live ? 'bg-emerald-400 shadow-[0_0_6px] shadow-emerald-400/60' : 'bg-slate-600'"
          ></span>
          {{ store.live ? '实时事件已连接' : '事件流已断开' }}
        </div>
        <div class="mt-1">v0.5.0 · MIT</div>
      </div>
    </aside>

    <!-- Main -->
    <div class="flex min-w-0 flex-1 flex-col">
      <header class="flex h-14 shrink-0 items-center gap-3 border-b border-[#1a2230] bg-[#0d131c] px-6">
        <h1 class="text-[15px] font-medium text-slate-100">{{ current?.label }}</h1>
        <div v-if="store.err" class="ml-4 truncate text-xs text-rose-400">{{ store.err }}</div>
        <div class="ml-auto flex items-center gap-4">
          <div class="hidden items-center gap-4 text-xs text-slate-500 md:flex">
            <span>在线客户端 <b class="mono text-slate-200">{{ store.stats.clients }}</b></span>
            <span>当前连接 <b class="mono text-slate-200">{{ store.stats.conn_active }}</b></span>
            <span>累计拦截 <b class="mono text-rose-400">{{ store.stats.blocked_total }}</b></span>
          </div>
          <div class="h-4 w-px bg-[#1f2937]"></div>
          <span class="text-xs text-slate-300">{{ store.username }}</span>
          <button
            class="rounded-lg border border-[#243044] px-2.5 py-1 text-xs text-slate-400 transition-colors hover:border-rose-500/40 hover:text-rose-400"
            @click="onLogout"
          >退出</button>
        </div>
      </header>
      <main class="min-h-0 flex-1 overflow-y-auto p-6">
        <Overview v-show="view === 'overview'" />
        <Tunnels v-show="view === 'tunnels'" />
        <Whitelist v-show="view === 'whitelist'" />
        <EventsLog v-show="view === 'events'" />
      </main>
    </div>
  </div>
</template>
