<script setup lang="ts">
import { computed, ref } from 'vue'
import { clearEvents, store } from '../store'

const paused = ref(false)
const keep = ref(0) // scroll anchor when paused

const kinds: Record<string, { label: string; dot: string }> = {
  blocked: { label: 'blocked', dot: 'bg-rose-500' },
  tls_fail: { label: 'tls_fail', dot: 'bg-rose-400' },
  conn_open: { label: 'conn_open', dot: 'bg-emerald-500' },
  conn_close: { label: 'conn_close', dot: 'bg-slate-600' },
  client_up: { label: 'client_up', dot: 'bg-sky-500' },
  client_down: { label: 'client_down', dot: 'bg-amber-500' },
  auth_fail: { label: 'auth_fail', dot: 'bg-orange-500' },
  login_fail: { label: 'login_fail', dot: 'bg-orange-500' },
}

const rows = computed(() => (paused.value ? store.events.slice(0, keep.value) : store.events))

function pauseToggle() {
  paused.value = !paused.value
  if (paused.value) keep.value = store.events.length
}

const counts = computed(() => {
  const c: Record<string, number> = {}
  for (const e of store.events) c[e.kind] = (c[e.kind] ?? 0) + 1
  return c
})
</script>

<template>
  <div class="mx-auto max-w-5xl">
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <span
        class="inline-flex items-center gap-1.5 rounded-full border border-[#1a2230] bg-[#0d131c] px-3 py-1 text-[11px] text-slate-400"
        v-for="(info, k) in kinds"
        :key="k"
      >
        <span class="inline-block h-1.5 w-1.5 rounded-full" :class="info.dot"></span>
        {{ info.label }}
        <b class="mono text-slate-200">{{ counts[k] ?? 0 }}</b>
      </span>
      <div class="ml-auto flex gap-2">
        <span v-if="!store.live" class="text-[11px] text-amber-400">事件流断开，自动重连中…</span>
        <button
          class="rounded-lg border border-[#243044] px-3 py-1.5 text-xs text-slate-400 transition-colors hover:text-slate-200"
          @click="pauseToggle"
        >{{ paused ? `恢复（保留 ${keep} 条）` : '暂停' }}</button>
        <button
          class="rounded-lg border border-[#243044] px-3 py-1.5 text-xs text-slate-400 transition-colors hover:border-rose-500/40 hover:text-rose-400"
          @click="clearEvents"
        >清空</button>
      </div>
    </div>

    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div v-if="rows.length" class="max-h-[65vh] divide-y divide-[#141c28] overflow-y-auto">
        <div v-for="(e, i) in rows" :key="rows.length - i" class="flex items-start gap-3 px-5 py-2.5 hover:bg-[#111827]/50">
          <span
            class="mt-1 inline-block h-2 w-2 shrink-0 rounded-full"
            :class="kinds[e.kind]?.dot ?? 'bg-slate-700'"
          ></span>
          <div class="min-w-0 flex-1">
            <div class="text-[13px] leading-relaxed" :class="e.cls">{{ e.text }}</div>
          </div>
          <span class="mono shrink-0 text-[11px] text-slate-600">{{ e.time }}</span>
        </div>
      </div>
      <div v-else class="px-5 py-16 text-center">
        <div class="text-3xl">🛡️</div>
        <div class="mt-3 text-sm text-slate-500">暂无安全事件</div>
        <div class="mt-1 text-[12px] text-slate-600">连接 / 关闭 / 白名单拦截都会实时流入此页</div>
      </div>
    </div>
  </div>
</template>
