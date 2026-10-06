<script setup lang="ts">
// 拦截记录：谁在敲我们的端口、被拒了多少次、要不要放行。
// 数据来自服务端 SQLite（web.db_path）的 refusals / refusals_recent，
// 刷新页面不清零；上方的实时事件流照旧，二者互补。
import { computed, onMounted, ref } from 'vue'
import { clearRefusals, refreshBlocked, store } from '../store'
import { api, ApiError, fmtFullTime } from '../api'

const onlyUncovered = ref(true) // 默认只看还没放行的（真正的外来敲击）
const tunnelFilter = ref('')
const detailOpen = ref(false)
const busyIP = ref('')
const msg = ref('')
const msgOk = ref(false)

const sum = computed(() => store.blocked.summary)
const rows = computed(() => store.blocked.rows)
const tunnels = computed(() => [...new Set(rows.value.map((r) => r.tunnel))].sort())
const filtered = computed(() =>
  rows.value.filter(
    (r) =>
      (!onlyUncovered.value || !r.covered) && (!tunnelFilter.value || r.tunnel === tunnelFilter.value),
  ),
)
const hiddenCount = computed(() => rows.value.filter((r) => r.covered).length)

function flash(ok: boolean, text: string) {
  msg.value = text
  msgOk.value = ok
  window.setTimeout(() => (msg.value = ''), 4000)
}

function kindOf(kind: string) {
  return kind === 'tls_fail'
    ? { label: 'TLS 握手失败', cls: 'text-amber-400' }
    : { label: '白名单拦截', cls: 'text-rose-400' }
}

async function allow(ip: string) {
  if (busyIP.value) return
  busyIP.value = ip
  try {
    await api.whitelistAdd(ip)
    await refreshBlocked() // 该行的 covered 会立刻翻成"已在白名单"
    flash(true, `已放行 ${ip} —— 对方重连即可进入（这是唯一能放行的动作）`)
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '放行失败')
  } finally {
    busyIP.value = ''
  }
}

async function copy(ip: string) {
  try {
    await navigator.clipboard.writeText(ip)
    flash(true, `已复制 ${ip}`)
  } catch {
    flash(false, '复制失败（浏览器不允许剪贴板）')
  }
}

async function doClear() {
  if (!window.confirm('清空全部拦截记录？白名单不受影响，只是把这张表归零。')) return
  try {
    await clearRefusals()
    flash(true, '已清空拦截记录')
  } catch (e) {
    flash(false, e instanceof ApiError ? e.message : '清空失败')
  }
}

onMounted(refreshBlocked)
</script>

<template>
  <div class="mx-auto max-w-5xl space-y-4 md:space-y-5">
    <!-- 汇总 -->
    <div class="grid grid-cols-2 gap-3 md:grid-cols-4 md:gap-4">
      <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4">
        <div class="text-[11px] tracking-wide text-slate-500">累计拦截</div>
        <div class="mono mt-2 text-2xl font-semibold text-rose-400">{{ sum.total }}</div>
        <div class="mt-1 text-[11px] text-slate-600">含 TLS 握手失败</div>
      </div>
      <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4">
        <div class="text-[11px] tracking-wide text-slate-500">独立来源 IP</div>
        <div class="mono mt-2 text-2xl font-semibold text-slate-200">{{ sum.unique_ips }}</div>
        <div class="mt-1 text-[11px] text-slate-600">去重后的敲击者</div>
      </div>
      <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4">
        <div class="text-[11px] tracking-wide text-slate-500">窗口内明细</div>
        <div class="mono mt-2 text-2xl font-semibold text-slate-200">{{ sum.recent }}</div>
        <div class="mt-1 text-[11px] text-slate-600">原始记录，保留 7 天</div>
      </div>
      <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4">
        <div class="text-[11px] tracking-wide text-slate-500">聚合条目</div>
        <div class="mono mt-2 text-2xl font-semibold text-slate-200">{{ sum.rows }}</div>
        <div class="mt-1 text-[11px]" :class="sum.dropped ? 'text-amber-400' : 'text-slate-600'">
          {{ sum.dropped ? `队列丢弃 ${sum.dropped}（扫描洪峰）` : '按 IP·隧道·端口 归并' }}
        </div>
      </div>
    </div>

    <!-- 工具条 -->
    <div class="rounded-xl border border-[#1a2230] bg-[#0d131c] p-4 md:p-5">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <label class="inline-flex cursor-pointer items-center gap-2 text-[12px] text-slate-400">
          <input v-model="onlyUncovered" type="checkbox" class="h-3.5 w-3.5 accent-emerald-500" />
          只看未放行
        </label>
        <select
          v-model="tunnelFilter"
          class="mono rounded-lg border border-[#233044] bg-[#0a0f17] px-2.5 py-1.5 text-[12px] text-slate-300 outline-none focus:border-emerald-500/50"
        >
          <option value="">全部隧道</option>
          <option v-for="t in tunnels" :key="t" :value="t">{{ t }}</option>
        </select>
        <button
          class="rounded-lg border border-[#243044] px-3 py-1.5 text-xs text-slate-400 transition-colors hover:text-slate-200"
          @click="detailOpen = !detailOpen"
        >{{ detailOpen ? '收起明细' : `展开明细（${store.blocked.recent.length}）` }}</button>
        <div class="ml-auto flex gap-2">
          <button
            class="rounded-lg border border-[#243044] px-3 py-1.5 text-xs text-slate-400 transition-colors hover:text-slate-200"
            @click="refreshBlocked"
          >刷新</button>
          <button
            class="rounded-lg border border-[#243044] px-3 py-1.5 text-xs text-slate-400 transition-colors hover:border-rose-500/40 hover:text-rose-400"
            @click="doClear"
          >清空记录</button>
        </div>
      </div>
      <div v-if="msg" class="mt-2.5 text-xs" :class="msgOk ? 'text-emerald-400' : 'text-rose-400'">{{ msg }}</div>
      <div v-if="onlyUncovered && hiddenCount" class="mt-2.5 text-[11px] text-slate-600">
        另有 {{ hiddenCount }} 条来自已在白名单的 IP（被别的规则挡住或历史遗留），取消勾选可见
      </div>
    </div>

    <!-- 聚合表 -->
    <div class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">被拦截的来源</span>
        <span class="mono text-[11px] text-slate-500">{{ filtered.length }} 条</span>
      </div>

      <!-- desktop -->
      <table class="hidden w-full text-left text-[13px] md:table">
        <thead>
          <tr class="text-[11px] uppercase tracking-wide text-slate-600">
            <th class="px-5 py-2 font-normal">来源 IP</th>
            <th class="px-3 py-2 font-normal">隧道</th>
            <th class="px-3 py-2 font-normal">原因</th>
            <th class="w-16 px-3 py-2 text-right font-normal">次数</th>
            <th class="px-3 py-2 font-normal">最近</th>
            <th class="px-3 py-2 font-normal">状态</th>
            <th class="w-28 px-5 py-2 text-right font-normal">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-[#151d29]">
          <tr v-for="r in filtered" :key="`${r.ip}|${r.tunnel}|${r.remote_port}|${r.kind}`" class="transition-colors hover:bg-[#111827]/60">
            <td class="px-5 py-3">
              <div class="mono text-slate-200">{{ r.ip }}</div>
              <div class="text-[11px] text-slate-600">{{ r.region || '未知地域' }}</div>
            </td>
            <td class="mono px-3 py-3 text-[12px] text-slate-400">{{ r.tunnel }} :{{ r.remote_port }}</td>
            <td class="px-3 py-3 text-[12px]" :class="kindOf(r.kind).cls">{{ kindOf(r.kind).label }}</td>
            <td class="mono px-3 py-3 text-right" :class="r.count > 50 ? 'text-rose-300' : 'text-slate-300'">{{ r.count }}</td>
            <td class="px-3 py-3 text-[11px] text-slate-500">
              <div>{{ fmtFullTime(r.last_seen) }}</div>
              <div class="text-slate-700">首次 {{ fmtFullTime(r.first_seen) }}</div>
            </td>
            <td class="px-3 py-3 text-[11px]">
              <span v-if="r.covered" class="text-emerald-400">已在白名单{{ r.rule ? `（${r.rule}）` : '' }}</span>
              <span v-else class="text-slate-500">未放行</span>
            </td>
            <td class="px-5 py-3 text-right">
              <div class="flex justify-end gap-1">
                <button
                  v-if="!r.covered"
                  :disabled="busyIP === r.ip"
                  class="rounded-md border border-emerald-500/30 px-2 py-1 text-xs text-emerald-300 transition-colors hover:bg-emerald-500/10 disabled:opacity-40"
                  title="把这个 IP 加入白名单（放行）"
                  @click="allow(r.ip)"
                >放行</button>
                <button
                  class="rounded-md border border-transparent px-2 py-1 text-xs text-slate-500 transition-colors hover:border-[#243044] hover:text-slate-300"
                  title="复制 IP"
                  @click="copy(r.ip)"
                >复制</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- mobile -->
      <ul class="divide-y divide-[#151d29] md:hidden">
        <li v-for="r in filtered" :key="`m-${r.ip}|${r.tunnel}|${r.remote_port}|${r.kind}`" class="px-4 py-3">
          <div class="flex items-start gap-2">
            <div class="min-w-0 flex-1">
              <div class="mono break-all text-[13px] text-slate-200">{{ r.ip }}</div>
              <div class="mt-0.5 text-[11px] text-slate-600">{{ r.region || '未知地域' }} · {{ r.tunnel }} :{{ r.remote_port }}</div>
            </div>
            <div class="mono shrink-0 text-right">
              <div class="text-[13px] text-rose-300">{{ r.count }} 次</div>
              <div class="text-[11px] text-slate-600">{{ fmtFullTime(r.last_seen).slice(5, 16) }}</div>
            </div>
          </div>
          <div class="mt-2 flex items-center gap-2 text-[11px]">
            <span :class="kindOf(r.kind).cls">{{ kindOf(r.kind).label }}</span>
            <span v-if="r.covered" class="text-emerald-400">已在白名单</span>
            <button
              v-if="!r.covered"
              :disabled="busyIP === r.ip"
              class="ml-auto rounded-md border border-emerald-500/30 px-2.5 py-1 text-[11px] text-emerald-300 disabled:opacity-40"
              @click="allow(r.ip)"
            >放行</button>
          </div>
        </li>
      </ul>

      <div v-if="!filtered.length" class="px-5 py-16 text-center">
        <div class="text-3xl">🛡️</div>
        <div class="mt-3 text-sm text-slate-500">{{ rows.length ? '当前筛选下没有记录' : '暂无拦截记录' }}</div>
        <div class="mt-1 text-[12px] text-slate-600">被白名单拒绝的连接会在这里长期留档（重启不丢）</div>
      </div>
    </div>

    <!-- 明细 -->
    <div v-if="detailOpen" class="overflow-hidden rounded-xl border border-[#1a2230] bg-[#0d131c]">
      <div class="flex items-center justify-between border-b border-[#1a2230] px-4 py-3 md:px-5">
        <span class="text-[13px] font-medium text-slate-300">最近拦截明细</span>
        <span class="mono text-[11px] text-slate-500">最新在前 · 保留 7 天</span>
      </div>
      <div class="max-h-[50vh] divide-y divide-[#151d29] overflow-y-auto">
        <div v-for="(h, i) in store.blocked.recent" :key="i" class="flex items-center gap-3 px-4 py-2 md:px-5">
          <span class="mt-0.5 inline-block h-1.5 w-1.5 shrink-0 rounded-full" :class="h.kind === 'tls_fail' ? 'bg-amber-400' : 'bg-rose-500'"></span>
          <span class="mono shrink-0 text-[12px] text-slate-300">{{ h.ip }}</span>
          <span class="mono min-w-0 flex-1 truncate text-[11px] text-slate-500">{{ h.tunnel }} :{{ h.remote_port }}<template v-if="h.reason"> · {{ h.reason }}</template></span>
          <span class="mono shrink-0 text-[11px] text-slate-600">{{ fmtFullTime(h.time) }}</span>
        </div>
        <div v-if="!store.blocked.recent.length" class="px-5 py-8 text-center text-[12px] text-slate-600">窗口内暂无明细</div>
      </div>
    </div>

    <p class="px-1 text-[11px] leading-relaxed text-slate-600">
      「拦截」= 来源 IP 未命中白名单（或在该端口上 TLS 握手失败）。记录存在服务端 SQLite（<span class="mono">web.db_path</span>），
      <span class="text-slate-400">重启服务、刷新页面都不会丢</span>；原始明细保留 7 天，聚合条目保留最近 5000 条。
      「放行」走白名单接口——放行后该 IP 会立刻在状态列翻绿，对方重连即通。想彻底清空只是把这张表归零，不影响白名单。
    </p>
  </div>
</template>