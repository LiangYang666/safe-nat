<script setup lang="ts">
import { computed, ref } from 'vue'
import { store } from '../store'
import { api, ApiError } from '../api'

const ruleInput = ref('')
const busy = ref(false)
const msg = ref('') // transient feedback
const msgOk = ref(false)

const rules = computed(() => store.rules)

function flash(ok: boolean, text: string) {
  msg.value = text
  msgOk.value = ok
  window.setTimeout(() => (msg.value = ''), 3000)
}

async function addRule() {
  const rule = ruleInput.value.trim()
  if (!rule || busy.value) return
  busy.value = true
  try {
    const created = await api.whitelistAdd(rule)
    ruleInput.value = ''
    await refresh()
    flash(true, `已加入 ${created.rule}`)
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

async function refresh() {
  try {
    store.rules = await api.whitelist()
  } catch {
    /* polling will retry */
  }
}

const hint = '精确 IP（1.2.3.4）或 CIDR（10.0.0.0/8），IPv4 / IPv6 均可'
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <!-- add form -->
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-5">
      <div class="text-[13px] font-medium text-slate-200">添加白名单规则</div>
      <div class="mt-1 text-[11px] text-slate-600">{{ hint }}</div>
      <form class="mt-3 flex gap-2" @submit.prevent="addRule">
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
      <span class="text-slate-500">为安全起见，删除规则前请确认你的当前 IP（可通过登录页来源地址核对）仍在白名单内。</span>
    </p>
  </div>
</template>
