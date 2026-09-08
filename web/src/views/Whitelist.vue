<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { store } from '../store'
import { api, ApiError } from '../api'

const ruleInput = ref('')
const busy = ref(false)
const msg = ref('') // transient feedback
const msgOk = ref(false)
const my = ref<{ ip: string; region?: string } | null>(null) // caller's own address

const rules = computed(() => store.rules)

function flash(ok: boolean, text: string) {
  msg.value = text
  msgOk.value = ok
  window.setTimeout(() => (msg.value = ''), 3000)
}

async function addRule(rule?: string) {
  const target = (rule ?? ruleInput.value).trim()
  if (!target || busy.value) return
  busy.value = true
  try {
    const created = await api.whitelistAdd(target)
    ruleInput.value = ''
    if (my.value && created.rule === my.value.ip) my.value = null // self already added
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
    flash(true, `已删除 ${rule}`)
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '删除失败')
  }
}

async function detectMe() {
  if (busy.value) return
  try {
    const info = await api.whitelistMe()
    my.value = info
    if (info.region) flash(true, `识别到 ${info.ip} · ${info.region}`)
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '识别失败')
  }
}

async function refresh() {
  try {
    store.rules = await api.whitelist()
  } catch {
    /* polling will retry */
  }
}

onMounted(detectMe)

const hint = '精确 IP（1.2.3.4）或 CIDR（10.0.0.0/8），IPv4 / IPv6 均可'
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <!-- add form -->
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-5">
      <div class="text-[13px] font-medium text-slate-200">添加白名单规则</div>
      <div class="mt-1 text-[11px] text-slate-600">{{ hint }}</div>

      <!-- detect-me card (LiangNat-style one-click self add) -->
      <div
        v-if="my"
        class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-emerald-500/20 bg-emerald-500/5 px-3.5 py-2.5"
      >
        <div class="min-w-0 text-[12px] text-slate-300">
          <span class="text-slate-500">当前访问者：</span>
          <span class="mono font-medium text-emerald-300">{{ my.ip }}</span>
          <span v-if="my.region" class="ml-2 text-slate-500">{{ my.region }}</span>
        </div>
        <button
          type="button"
          :disabled="busy"
          class="ml-auto rounded-md border border-emerald-500/40 px-3 py-1 text-[12px] font-medium text-emerald-300 transition-colors hover:bg-emerald-500/10 disabled:opacity-40"
          title="把当前访问 IP 加入白名单（如从手机流量访问本面板，加入的即手机 IP）"
          @click="addRule(my.ip)"
        >加入白名单</button>
      </div>

      <form class="mt-3 flex gap-2" @submit.prevent="addRule()">
        <input
          v-model="ruleInput"
          placeholder="例如 1.2.3.4 或 10.0.0.0/8"
          class="mono flex-1 rounded-lg border border-[#233044] bg-[#0a0f17] px-3.5 py-2 text-[13px] text-slate-200 placeholder-slate-600 outline-none transition-colors focus:border-emerald-500/50"
        />
        <button
          type="submit"
          :disabled="busy || !ruleInput.trim()"
          class="rounded-lg bg-emerald-500 px-5 py-2 text-[13px] font-medium text-emerald-950 transition-colors hover:bg-emerald-400 disabled:cursor-not-allowed disabled:opacity-40"
        >添加</button>
      </form>
      <div v-if="msg" class="mt-2.5 text-xs" :class="msgOk ? 'text-emerald-400' : 'text-rose-400'">{{ msg }}</div>
    </div>

    <!-- rule table -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-5 py-3">
        <span class="text-[13px] font-medium text-slate-300">规则列表</span>
        <span class="mono text-[11px] text-slate-500">{{ rules.length }} 条</span>
      </div>
      <table class="w-full text-left text-[13px]">
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
                title="删除该规则"
                @click="removeRule(r.id, r.rule)"
              >删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="!rules.length" class="px-5 py-12 text-center text-sm text-slate-600">
        白名单为空 —— 所有受防火墙保护的端口将拒绝一切连接
      </div>
    </div>

    <p class="px-1 text-[11px] leading-relaxed text-slate-600">
      防火墙规则全局生效：任何开启了 <span class="text-slate-400">firewall</span> 的隧道（含 SOCKS5 代理）在 accept 时校验来源 IP。
      添加时自动标注 IP 归属地（本地 ip2region 库，离线查询）。
      <span class="text-slate-500">手机流量访问本面板时，顶部会识别出你的公网 IP，点「加入白名单」即可放行自己。</span>
    </p>
  </div>
</template>
