<template>
  <div class="mx-auto max-w-5xl">
    <section class="mb-5 flex items-end justify-between gap-3 sm:mb-7">
      <div class="min-w-0">
        <p class="mb-1 text-xs font-extrabold uppercase tracking-[.18em] text-brand-500">重要日子</p>
        <h1 class="font-display text-2xl font-extrabold tracking-tight sm:text-3xl">倒数纪念日</h1>
        <p class="mt-1 text-[13px] text-muted-foreground sm:text-sm">
          {{ items.length ? `共 ${items.length} 个日子，最近的一个还有 ${nearestDays} 天` : '记录生日、纪念日与倒计时' }}
        </p>
      </div>
      <button class="btn-brand shrink-0" @click="openCreate">新建</button>
    </section>

    <section v-if="loading" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <div v-for="n in 3" :key="n" class="h-36 animate-pulse rounded-2xl bg-surface/80"></div>
    </section>

    <section v-else-if="sorted.length" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <article
        v-for="item in sorted"
        :key="item.id"
        class="cd-card group"
        :class="{ 'cd-card-past': item.days_left < 0 }"
        :style="{ '--cd': colorHex(item.color) }"
      >
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <p class="cd-days">{{ Math.abs(item.days_left) }}</p>
            <p class="cd-unit">{{ dayLabel(item) }}</p>
          </div>
          <div class="flex shrink-0 gap-1 opacity-0 transition group-hover:opacity-100 sm:opacity-100">
            <button class="cd-icon" aria-label="编辑" @click="openEdit(item)">编辑</button>
            <button class="cd-icon cd-icon-danger" aria-label="删除" @click="askDelete(item)">删除</button>
          </div>
        </div>
        <h3 class="cd-title" :title="item.title">{{ item.title }}</h3>
        <p class="cd-meta">{{ nextText(item) }}</p>
        <p v-if="item.notes" class="cd-notes" :title="item.notes">{{ item.notes }}</p>
      </article>
    </section>

    <section v-else class="empty-state">
      <p class="text-base font-bold text-foreground">还没有记录任何日子</p>
      <p class="mt-1 text-sm text-muted-foreground">生日、纪念日、考试倒计时都可以放进来，它会自动算出还有多少天。</p>
      <button class="btn-brand mt-4" @click="openCreate">添加第一个</button>
    </section>

    <!-- 新建 / 编辑 -->
    <Teleport to="body">
      <div v-if="formOpen" class="fixed inset-0 z-[110] flex items-center justify-center bg-slate-950/40 p-4 backdrop-blur-sm" @click.self="formOpen = false">
        <form class="surface-modal w-full max-w-md rounded-[26px] p-5 sm:p-6" @submit.prevent="submit">
          <div class="flex items-start justify-between gap-4">
            <h2 class="text-lg font-extrabold text-foreground">{{ form.id ? '编辑日子' : '新建日子' }}</h2>
            <button type="button" class="icon-button" aria-label="关闭" @click="formOpen = false">×</button>
          </div>

          <label class="mt-4 block">
            <span class="field-label">名称</span>
            <input v-model.trim="form.title" class="compact-input mt-1.5 w-full" maxlength="60" placeholder="例如：结婚纪念日" />
          </label>

          <label class="mt-3 block">
            <span class="field-label">日期</span>
            <input v-model="form.target_date" type="date" class="compact-input mt-1.5 w-full" required />
          </label>

          <div class="mt-3 flex flex-col gap-2">
            <label class="flex items-center gap-2 text-sm font-semibold text-foreground">
              <input v-model="form.repeat_yearly" type="checkbox" class="h-4 w-4 accent-brand-500" />
              每年重复（生日、纪念日）
            </label>
            <label v-if="form.repeat_yearly" class="flex items-center gap-2 text-sm font-semibold text-foreground">
              <input v-model="form.lunar" type="checkbox" class="h-4 w-4 accent-brand-500" />
              按农历计算
            </label>
          </div>

          <label class="mt-3 block">
            <span class="field-label">备注 <small class="font-normal text-muted-foreground">可选</small></span>
            <textarea v-model.trim="form.notes" class="compact-input mt-1.5 w-full" rows="2" maxlength="500" placeholder="想记住的话"></textarea>
          </label>

          <div class="mt-3">
            <span class="field-label">颜色</span>
            <div class="mt-2 flex flex-wrap gap-2">
              <button
                v-for="chip in palette"
                :key="chip.key"
                type="button"
                class="h-7 w-7 rounded-full ring-2 ring-offset-2 transition"
                :class="form.color === chip.key ? 'ring-foreground/40' : 'ring-transparent'"
                :style="{ background: chip.hex }"
                :aria-label="chip.label"
                @click="form.color = chip.key"
              ></button>
            </div>
          </div>

          <div class="mt-5 flex gap-2.5">
            <button type="button" class="btn-ghost flex-1" @click="formOpen = false">取消</button>
            <button type="submit" class="btn-brand flex-1" :disabled="saving || !form.title || !form.target_date">
              {{ saving ? '保存中…' : '保存' }}
            </button>
          </div>
        </form>
      </div>
    </Teleport>

    <ConfirmDialog
      v-model="confirmOpen"
      title="删除这个日子？"
      :message="pending ? `「${pending.title}」将被移除，此操作不可撤销。` : ''"
      confirm-text="删除"
      danger
      :loading="saving"
      @confirm="confirmDelete"
    />

    <Toast :message="toast.message" :type="toast.type" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Toast from '../components/Toast.vue'
import {
  createCountdown, deleteCountdown, getCountdowns, updateCountdown,
  type CountdownItem,
} from '../api/countdown'

const palette = [
  { key: 'rose', label: '玫瑰', hex: '#f43f5e' },
  { key: 'amber', label: '琥珀', hex: '#f59e0b' },
  { key: 'emerald', label: '翡翠', hex: '#10b981' },
  { key: 'sky', label: '天蓝', hex: '#0ea5e9' },
  { key: 'violet', label: '紫罗兰', hex: '#8b5cf6' },
  { key: 'slate', label: '石墨', hex: '#64748b' },
]

const items = ref<CountdownItem[]>([])
const loading = ref(true)
const saving = ref(false)
const formOpen = ref(false)
const confirmOpen = ref(false)
const pending = ref<CountdownItem | null>(null)
const toast = reactive<{ message: string; type: 'success' | 'error' }>({ message: '', type: 'success' })

function todayKey(): string {
  return new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Shanghai' }).format(new Date())
}

const form = reactive({
  id: 0,
  title: '',
  target_date: todayKey(),
  repeat_yearly: true,
  lunar: false,
  notes: '',
  color: 'rose',
})

// 未来的排在前面（越近越靠前），已经过去的沉到最后。
const sorted = computed(() => [...items.value].sort((a, b) => {
  const aPast = a.days_left < 0 ? 1 : 0
  const bPast = b.days_left < 0 ? 1 : 0
  if (aPast !== bPast) return aPast - bPast
  return a.days_left - b.days_left
}))

const nearestDays = computed(() => {
  const upcoming = items.value.filter(item => item.days_left >= 0).map(item => item.days_left)
  return upcoming.length ? Math.min(...upcoming) : 0
})

function colorHex(key: string): string {
  return palette.find(chip => chip.key === key)?.hex || palette[0].hex
}

function dayLabel(item: CountdownItem): string {
  if (item.days_left > 0) return '天后'
  if (item.days_left === 0) return '就是今天'
  return '天前'
}

function nextText(item: CountdownItem): string {
  if (!item.next_date) return ''
  const weekday = new Intl.DateTimeFormat('zh-CN', { weekday: 'short', timeZone: 'UTC' })
    .format(new Date(`${item.next_date}T00:00:00Z`))
  const repeat = item.repeat_yearly ? (item.calendar === 'lunar' ? ' · 农历每年' : ' · 每年') : ''
  return `${item.next_date} ${weekday}${repeat}`
}

async function load() {
  loading.value = true
  try {
    const res = await getCountdowns()
    items.value = res.data?.data || []
  } catch (err: any) {
    showToast(err.response?.data?.message || '读取失败', 'error')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  Object.assign(form, {
    id: 0, title: '', target_date: todayKey(),
    repeat_yearly: true, lunar: false, notes: '', color: 'rose',
  })
  formOpen.value = true
}

function openEdit(item: CountdownItem) {
  Object.assign(form, {
    id: item.id,
    title: item.title,
    target_date: item.target_date.slice(0, 10),
    repeat_yearly: item.repeat_yearly,
    lunar: item.calendar === 'lunar',
    notes: item.notes,
    color: item.color,
  })
  formOpen.value = true
}

async function submit() {
  if (saving.value || !form.title || !form.target_date) return
  saving.value = true
  try {
    const payload = {
      title: form.title,
      target_date: form.target_date,
      repeat_yearly: form.repeat_yearly,
      // 不重复时农历没有意义，后端也会归一化成公历。
      calendar: form.repeat_yearly && form.lunar ? 'lunar' : 'solar',
      notes: form.notes,
      color: form.color,
    }
    if (form.id) await updateCountdown(form.id, payload)
    else await createCountdown(payload)
    formOpen.value = false
    await load()
    showToast(form.id ? '已更新' : '已添加')
  } catch (err: any) {
    showToast(err.response?.data?.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

function askDelete(item: CountdownItem) {
  pending.value = item
  confirmOpen.value = true
}

async function confirmDelete() {
  if (!pending.value || saving.value) return
  saving.value = true
  try {
    await deleteCountdown(pending.value.id)
    confirmOpen.value = false
    pending.value = null
    await load()
    showToast('已删除')
  } catch (err: any) {
    showToast(err.response?.data?.message || '删除失败', 'error')
  } finally {
    saving.value = false
  }
}

function showToast(message: string, type: 'success' | 'error' = 'success') {
  toast.message = message
  toast.type = type
  window.setTimeout(() => { toast.message = '' }, 2600)
}

onMounted(load)
</script>

<style scoped>
.cd-card { @apply relative overflow-hidden rounded-2xl border border-border/70 bg-surface/80 p-4 transition hover:-translate-y-0.5 hover:shadow-lg; }
.cd-card::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 4px; background: var(--cd); }
.cd-card-past { @apply opacity-70; }
.cd-days { @apply font-display text-4xl font-extrabold leading-none tabular-nums; color: var(--cd); }
.cd-unit { @apply mt-1 text-[11px] font-bold text-muted-foreground; }
.cd-title { @apply mt-3 truncate text-base font-extrabold text-foreground; }
.cd-meta { @apply mt-1 text-[11px] font-medium text-muted-foreground; }
.cd-notes { @apply mt-2 line-clamp-2 text-xs leading-5 text-muted-foreground; }
.cd-icon { @apply rounded-lg border border-border bg-surface px-2 py-1 text-[11px] font-semibold text-muted-foreground transition hover:bg-muted hover:text-foreground; }
.cd-icon-danger { @apply hover:border-rose-300 hover:text-rose-500; }
</style>
