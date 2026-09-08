<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { store } from '../store'
import { api, ApiError } from '../api'

const ruleInput = ref('')
const busy = ref(false)
const msg = ref('') // transient feedback
const msgOk = ref(false)
const my = ref<{ ip: string; region?: string; covered?: boolean; rule?: string } | null>(null)

const rules = computed(() => store.rules)
const isV4 = computed(() => !!my.value && !my.value.ip.includes(':'))
const covered = computed(() => !!my.value?.covered)

function flash(ok: boolean, text: string) {
  msg.value = text
  msgOk.value = ok
  window.setTimeout(() => (msg.value = ''), 3000)
}

// build <ip>/<bits> CIDR from the caller's own IPv4 (e.g. 1.2.3.4 → 1.2.0.0/16)
function cidrOf(ip: string, bits: number): string {
  const parts = ip.split('.').map(Number)
  const full = bits / 8
  for (let i = full; i < 4; i++) parts[i] = 0
  return `${parts.join('.')}/${bits}`
}

async function addRule(rule?: string) {
  const target = (rule ?? ruleInput.value).trim()
  if (!target || busy.value) return
  busy.value = true
  try {
    const created = await api.whitelistAdd(target)
    ruleInput.value = ''
    if (my.value) {
      // if what we just added covers our own IP, flip the status card to green
      const m = await api.whitelistMe().catch(() => null)
      if (m) my.value = m
    }
    await refresh()
    flash(true, `已加入 ${created.rule}${created.region ? ' · ' + created.region : ''}`)
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '添加失败')
  } finally {
    busy.value = false
  }
}

async function removeRule(id: number, rule: string) {
  try {
    await api.whitelistDelete(id)
    await refresh()
    if (my.value) {
      const m = await api.whitelistMe().catch(() => null)
      if (m) my.value = m // deleting may un-cover our own IP → card flips to red
    }
    flash(true, `已删除 ${rule}`)
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '删除失败')
  }
}

async function detectMe() {
  try {
    my.value = await api.whitelistMe()
  } catch {
    /* page-level polling handles it */
  }
}

async function refresh() {
  try {
    store.rules = await api.whitelist()
  } catch {
    /* polling will retry */
  }
}

onMounted(() => {
  detectMe()
  refresh()
})

const hint = '精确 IP（1.2.3.4）或 CIDR（10.0.0.0/8），IPv4 / IPv6 均可'
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-4 md:space-y-5">
    <!-- my-IP status card: green when covered, red when not -->
    <div
      v-if="my"
      class="rounded-xl border px-4 py-3 md:px-5 md:py-4"
      :class="covered
        ? 'border-emerald-500/30 bg-emerald-500/[0.06]'
        : 'border-rose-500/30 bg-rose-500/[0.05]'"
    >
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span
          class="mono rounded-md px-2 py-0.5 text-[13px] font-semibold md:text-sm"
          :class="covered ? 'bg-emerald-500/15 text-emerald-300' : 'bg-rose-500/15 text-rose-300'"
        >{{ my.ip }}</span>
        <span v-if="my.region" class="text-[11px] text-slate-500 md:text-xs">{{ my.region }}</span>
        <span
          v-if="covered"
          class="ml-auto inline-flex items-center gap-1.5 rounded-full border border-emerald-500/30 px-2.5 py-0.5 text-[11px] text-emerald-300"
        >
          <i class="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400"></i>
          已在白名单<template v-if="my.rule">（命中 {{ my.rule }}）</template>
        </span>
        <span v-else class="ml-auto inline-flex items-center gap-1.5 rounded-full border border-rose-500/30 px-2.5 py-0.5 text-[11px] text-rose-300">
          <i class="inline-block h-1.5 w-1.5 rounded-full bg-rose-400"></i>
          未在白名单
        </span>
      </div>

      <!-- quick add actions (only when NOT covered) -->
      <div v-if="!covered" class="mt-3 flex flex-wrap gap-1.5 border-t border-white/5 pt-3">
        <button
          :disabled="busy"
          class="rounded-lg bg-emerald-500 px-3 py-1.5 text-[12px] font-medium text-emerald-950 transition-colors hover:bg-emerald-400 disabled:opacity-40"
          title="只放行当前 IP（推荐）"
          @click="addRule(my.ip)"
        >加精确 IP</button>
        <button
          v-if="isV4"
          :disabled="busy"
          class="rounded-lg border border-emerald-500/40 px-3 py-1.5 text-[12px] text-emerald-300 transition-colors hover:bg-emerald-500/10 disabled:opacity-40"
          title="放行当前 /24 网段（如移动网络整段出口）"
          @click="addRule(cidrOf(my.ip, 24))"
        >加 /24 网段</button>
        <button
          v-if="isV4"
          :disabled="busy"
          class="rounded-lg border border-emerald-500/40 px-3 py-1.5 text-[12px] text-emerald-300 transition-colors hover:bg-emerald-500/10 disabled:opacity-40"
          title="放行当前 /16 大网段（宽泛，慎用）"
          @click="addRule(cidrOf(my.ip, 16))"
        >加 /16 网段</button>
        <span class="ml-auto self-center text-[11px] text-slate-600">加宽网段仅当你常换出口 IP 时用</span>
      </div>
    </div>

    <!-- add form -->
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4 md:p-5">
      <div class="text-[13px] font-medium text-slate-200">手动添加规则</div>
      <div class="mt-1 text-[11px] text-slate-600">{{ hint }}</div>
      <form class="mt-3 flex gap-2" @submit.prevent="addRule()">
        <input
          v-model="ruleInput"
          placeholder="1.2.3.4 或 10.0.0.0/8"
          class="mono min-w-0 flex-1 rounded-lg border border-[#233044] bg-[#0a0f17] px-3.5 py-2 text-[13px] text-slate-200 placeholder-slate-600 outline-none transition-colors focus:border-emerald-500/50"
        />
        <button
          type="submit"
          :disabled="busy || !ruleInput.trim()"
          class="shrink-0 rounded-lg bg-emerald-500 px-4 py-2 text-[13px] font-medium text-emerald-950 transition-colors hover:bg-emerald-400 disabled:cursor-not-allowed disabled:opacity-40"
        >添加</button>
      </form>
      <div v-if="msg" class="mt-2.5 text-xs" :class="msgOk ? 'text-emerald-400' : 'text-rose-400'">{{ msg }}</div>
    </div>

    <!-- rule list: table (md+) / cards (mobile) -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">规则列表</span>
        <span class="mono text-[11px] text-slate-500">{{ rules.length }} 条</span>
      </div>

      <!-- desktop table -->
      <table class="hidden w-full text-left text-[13px] md:table">
        <tbody class="divide-y divide-[#151d29]">
          <tr v-for="r in rules" :key="r.id" class="transition-colors hover:bg-[#111827]/60">
            <td class="mono w-14 px-5 py-3 text-slate-600">#{{ r.id }}</td>
            <td class="mono px-3 py-3 text-slate-200">{{ r.rule }}</td>
            <td class="px-3 py-3 text-[12px] text-slate-500">{{ r.region || '—' }}</td>
            <td class="px-3 py-3 text-right">
              <span class="text-[11px] text-slate-600">{{ r.created_at.slice(11, 19) }}</span>
            </td>
            <td class="w-16 px-4 py-3 text-right">
              <button
                class="rounded-md border border-transparent px-2 py-1 text-xs text-slate-500 transition-colors hover:border-rose-500/30 hover:bg-rose-500/10 hover:text-rose-400"
                @click="removeRule(r.id, r.rule)"
              >删除</button>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- mobile cards -->
      <ul class="divide-y divide-[#151d29] md:hidden">
        <li v-for="r in rules" :key="r.id" class="px-4 py-3">
          <div class="flex items-center gap-2">
            <span class="mono min-w-0 flex-1 break-all text-[13px] text-slate-200">{{ r.rule }}</span>
            <button
              class="shrink-0 rounded-md border border-transparent px-2 py-1 text-xs text-slate-500 transition-colors hover:border-rose-500/30 hover:bg-rose-500/10 hover:text-rose-400"
              @click="removeRule(r.id, r.rule)"
            >删除</button>
          </div>
          <div class="mt-1 flex items-center gap-2 text-[11px] text-slate-600">
            <span class="min-w-0 flex-1 truncate">{{ r.region || '—' }}</span>
            <span class="shrink-0">#{{ r.id }} · {{ r.created_at.slice(5, 16).replace('T', ' ') }}</span>
          </div>
        </li>
      </ul>

      <div v-if="!rules.length" class="px-5 py-12 text-center text-sm text-slate-600">
        白名单为空 —— 所有受防火墙保护的端口将拒绝一切连接
      </div>
    </div>

    <p class="px-1 text-[11px] leading-relaxed text-slate-600">
      防火墙规则全局生效：任何开启了 <span class="text-slate-400">firewall</span> 的隧道（含 SOCKS5 代理）在 accept 时校验来源 IP。
      添加时自动标注 IP 归属地（本地 ip2region 库，离线查询）。手机流量访问本面板时顶部会显示你的公网 IP 状态。
    </p>
  </div>
</template>
