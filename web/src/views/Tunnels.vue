<script setup lang="ts">
import { computed } from 'vue'
import { store } from '../store'

const rows = computed(() =>
  [...store.tunnels].sort((a, b) => a.remote_port - b.remote_port),
)

function peerText(t: (typeof rows.value)[number]): string {
  return t.type === 'socks5' ? '动态（CONNECT 目标）' : '由客户端拨号'
}
</script>

<template>
  <div class="mx-auto max-w-6xl">
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] overflow-hidden">
      <div class="overflow-x-auto">
      <table class="w-full min-w-[640px] text-left text-[13px]">
        <thead>
          <tr class="border-b border-[#1a2230] text-[11px] uppercase tracking-wider text-slate-500">
            <th class="px-5 py-3 font-medium">名称</th>
            <th class="px-3 py-3 font-medium">类型</th>
            <th class="px-3 py-3 font-medium">公网端口</th>
            <th class="px-3 py-3 font-medium">防火墙</th>
            <th class="px-3 py-3 font-medium">加密</th>
            <th class="px-3 py-3 font-medium">归属客户端</th>
            <th class="px-3 py-3 font-medium">当前/累计连接</th>
            <th class="px-3 py-3 font-medium text-right">拦截</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-[#151d29]">
          <tr v-for="t in rows" :key="t.remote_port + t.client" class="transition-colors hover:bg-[#111827]/60">
            <td class="px-5 py-3">
              <div class="font-medium text-slate-200">{{ t.name }}</div>
              <div class="mono mt-0.5 text-[11px] text-slate-600">{{ peerText(t) }}</div>
            </td>
            <td class="px-3 py-3">
              <span
                class="rounded px-1.5 py-0.5 text-[10px] uppercase tracking-wide"
                :class="t.type === 'socks5' ? 'bg-violet-500/10 text-violet-300' : 'bg-sky-500/10 text-sky-300'"
              >{{ t.type }}</span>
            </td>
            <td class="mono px-3 py-3 text-slate-300">:{{ t.remote_port }}</td>
            <td class="px-3 py-3">
              <span
                class="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px]"
                :class="t.firewall
                  ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-300'
                  : 'border-amber-500/25 bg-amber-500/10 text-amber-300'"
              >
                <span class="inline-block h-1.5 w-1.5 rounded-full" :class="t.firewall ? 'bg-emerald-400' : 'bg-amber-400'"></span>
                {{ t.firewall ? '白名单保护' : '开放' }}
              </span>
            </td>
            <td class="px-3 py-3">
              <span
                v-if="t.tls"
                class="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] border-teal-500/25 bg-teal-500/10 text-teal-300"
              >
                <span class="inline-block h-1.5 w-1.5 rounded-full bg-teal-400"></span>
                TLS
              </span>
              <span v-else class="text-[11px] text-slate-600">明文</span>
            </td>
            <td class="px-3 py-3 text-slate-400">{{ t.client }}</td>
            <td class="mono px-3 py-3">
              <span class="text-emerald-400">{{ t.conn_active }}</span>
              <span class="text-slate-600"> / {{ t.conn_total }}</span>
            </td>
            <td class="mono px-3 py-3 text-right">
              <span :class="t.blocked ? 'text-rose-400' : 'text-slate-700'">{{ t.blocked }}</span>
            </td>
          </tr>
        </tbody>
      </table>
      </div>
      <div v-if="!rows.length" class="px-5 py-12 text-center text-sm text-slate-600">
        暂无隧道 —— 客户端上线并注册端口后会出现在这里
      </div>
    </div>

    <p class="mt-3 px-1 text-[11px] leading-relaxed text-slate-600">
      隧道清单由客户端登录时注册（config_client.yaml）。<span class="text-slate-500">firewall = 白名单保护</span>：公网连接在 accept 时按来源 IP 校验，命中即放行；变更白名单对已建立连接不生效（v1 语义）。
    </p>
  </div>
</template>
