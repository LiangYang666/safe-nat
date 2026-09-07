<script setup lang="ts">
import { ref } from 'vue'
import { ApiError } from '../api'

const props = defineProps<{ err: string }>()
const emit = defineEmits<{ (e: 'login', u: string, p: string): void }>()

const username = ref('')
const password = ref('')
const busy = ref(false)
const localErr = ref('')

async function submit() {
  if (busy.value) return
  busy.value = true
  localErr.value = ''
  try {
    emit('login', username.value.trim(), password.value)
  } catch (e) {
    localErr.value = e instanceof ApiError ? e.message : '登录失败'
  } finally {
    busy.value = false
    password.value = ''
  }
}
</script>

<template>
  <div class="grid h-full place-items-center bg-[radial-gradient(ellipse_at_top,#0e1622_0%,#080c12_60%)]">
    <div class="w-[360px] rounded-2xl border border-[#1c2636] bg-[#0d131c]/90 p-8 shadow-2xl shadow-black/50">
      <div class="mb-8 flex flex-col items-center gap-2">
        <div class="grid h-12 w-12 place-items-center rounded-xl bg-emerald-500/15 text-emerald-400">
          <svg class="h-7 w-7" viewBox="0 0 24 24" fill="currentColor">
            <path d="M12 2l9 5v5.5c0 5.2-3.7 9.3-9 10.5-5.3-1.2-9-5.3-9-10.5V7l9-5z" />
          </svg>
        </div>
        <div class="mt-2 text-center">
          <div class="text-lg font-semibold tracking-wide text-slate-100">safe-nat</div>
          <div class="mt-0.5 text-[11px] text-slate-500">NAT 穿透 · IP 白名单防火墙管理</div>
        </div>
      </div>

      <form class="space-y-3" @submit.prevent="submit">
        <input
          v-model="username"
          type="text"
          autocomplete="username"
          placeholder="用户名"
          class="w-full rounded-lg border border-[#233044] bg-[#0a0f17] px-3.5 py-2.5 text-sm text-slate-200 placeholder-slate-600 outline-none transition-colors focus:border-emerald-500/50"
        />
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          placeholder="密码"
          class="w-full rounded-lg border border-[#233044] bg-[#0a0f17] px-3.5 py-2.5 text-sm text-slate-200 placeholder-slate-600 outline-none transition-colors focus:border-emerald-500/50"
        />
        <div v-if="localErr || err" class="pt-1 text-center text-xs text-rose-400">
          {{ localErr || err }}
        </div>
        <button
          type="submit"
          :disabled="busy || !username || !password"
          class="mt-2 w-full rounded-lg bg-emerald-500 py-2.5 text-sm font-medium text-emerald-950 transition-colors hover:bg-emerald-400 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {{ busy ? '登录中…' : '登 录' }}
        </button>
      </form>

      <p class="mt-6 text-center text-[11px] leading-relaxed text-slate-600">
        凭据在服务器配置中设置（web.username / web.password）<br />
        登录失败 5 次将临时锁定
      </p>
    </div>
  </div>
</template>
