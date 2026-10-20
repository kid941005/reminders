<template>
  <div class="pb-6">
    <!-- 头部：范围标题 + 翻页 -->
    <section class="mb-4 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <p class="text-[11px] font-bold uppercase tracking-[.16em] text-brand-500">{{ isWeek ? '周视图' : '月视图' }}</p>
        <h1 class="mt-1 text-2xl font-extrabold text-foreground sm:text-3xl">{{ rangeLabel }}</h1>
        <p class="mt-1 text-xs text-muted-foreground">
          共 {{ totalItems }} 项提醒<span v-if="completedCount">，已完成 {{ completedCount }} 项</span>
          <span v-if="uncoveredYears.length" class="font-semibold text-amber-600 dark:text-amber-400">
            · {{ uncoveredYears.join('、') }} 年放假安排尚未公布
          </span>
        </p>
      </div>
      <div class="flex items-center gap-2">
        <button class="cal-nav" :aria-label="isWeek ? '上一周' : '上一月'" @click="shift(-1)">‹</button>
        <button class="cal-today" @click="goToday">今天</button>
        <button class="cal-nav" :aria-label="isWeek ? '下一周' : '下一月'" @click="shift(1)">›</button>
      </div>
    </section>

    <!-- 加载骨架 -->
    <section v-if="loading" class="space-y-2">
      <div v-for="n in 6" :key="n" class="h-16 animate-pulse rounded-2xl bg-surface/80"></div>
    </section>

    <!-- 周视图：桌面 7 列，手机单列堆叠（避免 7 列挤成一条线） -->
    <section v-else-if="isWeek" class="grid gap-2 sm:grid-cols-7 sm:gap-1.5">
      <article
        v-for="day in days"
        :key="day.key"
        class="week-col"
        :class="{ 'week-col-today': day.isToday }"
      >
        <button type="button" class="week-head" @click="openDay(day)">
          <span class="week-name">{{ day.weekdayLabel }}</span>
          <strong class="week-num">{{ day.dayNum }}</strong>
          <span
            v-if="day.holiday"
            class="holiday-badge-inline"
            :class="day.holiday.off ? 'holiday-off' : 'holiday-work'"
            :title="`${day.holiday.name}${day.holiday.off ? ' 放假' : ' 调休上班'}`"
          >{{ day.holiday.off ? '休' : '班' }}</span>
          <span v-if="day.items.length" class="week-count">{{ day.items.length }}</span>
        </button>
        <p
          v-if="day.festival || day.term"
          class="week-fest"
          :class="{ 'week-fest-term': !day.festival }"
          :title="day.festival || day.term"
        >{{ day.festival || day.term }}</p>
        <div class="week-body">
          <button
            v-for="item in day.items"
            :key="item.id"
            type="button"
            class="cal-chip"
            :class="{ 'cal-chip-done': !!item.completed_at }"
            :title="`${item.title}（点击查看当天）`"
            @click="openDay(day)"
          >
            <span class="cal-chip-time">{{ timeLabel(item) }}</span>
            <span class="cal-chip-title">{{ item.title }}</span>
          </button>
          <p v-if="!day.items.length" class="week-empty">无</p>
        </div>
      </article>
    </section>

    <!-- 月视图：6×7 网格 -->
    <section v-else class="month-wrap">
      <div class="month-weekdays">
        <span v-for="label in weekdayLabels" :key="label">{{ label }}</span>
      </div>
      <div class="month-grid">
        <button
          v-for="day in days"
          :key="day.key"
          type="button"
          class="month-cell"
          :class="{ 'month-cell-today': day.isToday, 'month-cell-outside': !day.inMonth }"
          @click="openDay(day)"
        >
          <span
            class="month-num"
            :class="{
              'month-num-today': day.isToday,
              'month-num-festival': !day.isToday && day.festival,
              'month-num-term': !day.isToday && !day.festival && day.term,
            }"
          >{{ day.dayNum }}</span>
          <span
            v-if="day.holiday"
            class="holiday-badge"
            :class="day.holiday.off ? 'holiday-off' : 'holiday-work'"
            :title="`${day.holiday.name}${day.holiday.off ? ' 放假' : ' 调休上班'}`"
          >{{ day.holiday.off ? '休' : '班' }}</span>
          <!-- 节日优先于节气：同一天既是节气又是节日时只显示节日 -->
          <span
            v-if="day.festival || day.term"
            class="month-fest"
            :class="{ 'month-fest-term': !day.festival }"
            :title="day.festival || day.term"
          >{{ day.festival || day.term }}</span>
          <!-- 桌面显示标题，手机只显示圆点，否则格子会被文字撑爆 -->
          <span class="hidden sm:flex sm:flex-col sm:gap-0.5 sm:w-full">
            <span
              v-for="item in day.items.slice(0, 2)"
              :key="item.id"
              class="month-item"
              :class="{ 'month-item-done': !!item.completed_at }"
            >
              <span class="month-dot"></span>
              <span class="truncate">{{ item.title }}</span>
            </span>
            <span v-if="day.items.length > 2" class="month-more">+{{ day.items.length - 2 }} 项</span>
          </span>
          <span v-if="day.items.length" class="mt-1 flex flex-wrap gap-0.5 sm:hidden">
            <span
              v-for="n in Math.min(day.items.length, 4)"
              :key="n"
              class="month-pip"
              :class="{ 'month-pip-done': !!day.items[n - 1]?.completed_at }"
            ></span>
          </span>
        </button>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getHolidays, getLunarCalendar, getReminders, type HolidayDay, type ReminderItem } from '../api/reminder'

const route = useRoute()
const router = useRouter()

const isWeek = computed(() => route.name === 'WeekView')
const weekdayLabels = ['一', '二', '三', '四', '五', '六', '日']

// 全部日期运算都在「日期字符串 + UTC 零点」上做：日历只关心年月日，
// 用 UTC 可以避开本地时区在夏令时/跨时区旅行时把日期挪一天。
// 取「今天」时显式指定 Asia/Shanghai，与后端的今天边界保持一致。
const shanghaiDay = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Shanghai' })
const timeFmt = new Intl.DateTimeFormat('zh-CN', {
  hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'Asia/Shanghai',
})

function todayKey(): string {
  return shanghaiDay.format(new Date())
}

function parseKey(key: string): Date {
  const [y, m, d] = key.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d))
}

function keyOf(date: Date): string {
  return date.toISOString().slice(0, 10)
}

function addDays(date: Date, days: number): Date {
  return new Date(date.getTime() + days * 86400000)
}

// 周一为一周起点：getUTCDay() 周日为 0，转成 0=周一。
function weekdayIndex(date: Date): number {
  return (date.getUTCDay() + 6) % 7
}

const anchor = ref(todayKey())
const items = ref<ReminderItem[]>([])
const loading = ref(true)

// 月视图的节日数据。后端 DayInfo 已经带 festival（国际节日 + 农历传统节日）与
// term（节气），直接复用农历接口即可，前端不再维护第二份节日表。
type DayFestival = { festival?: string; term?: string; lunar?: string }
const festivalByDay = ref(new Map<string, DayFestival>())

// 法定节假日（放假 / 调休补班）。与节日不同，这份数据来自国务院年度公告，
// 只能查表；未覆盖的年份要明确提示，不能当作普通工作日。
const holidayByDay = ref(new Map<string, HolidayDay>())
const holidayMinYear = ref(0)
const holidayMaxYear = ref(0)

async function loadHolidays() {
  try {
    const res = await getHolidays(keyOf(range.value.start), keyOf(range.value.end))
    const data = res.data?.data
    const next = new Map<string, HolidayDay>()
    for (const [date, day] of Object.entries((data?.days || {}) as Record<string, HolidayDay>)) {
      next.set(date, day)
    }
    holidayByDay.value = next
    holidayMinYear.value = data?.min_year || 0
    holidayMaxYear.value = data?.max_year || 0
  } catch {
    holidayByDay.value = new Map()
  }
}

async function loadFestivals() {
  const months = new Set<string>()
  const start = range.value.start
  let cursor = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth(), 1))
  while (cursor.getTime() < range.value.end.getTime()) {
    months.add(`${cursor.getUTCFullYear()}-${cursor.getUTCMonth() + 1}`)
    cursor = new Date(Date.UTC(cursor.getUTCFullYear(), cursor.getUTCMonth() + 1, 1))
  }
  const next = new Map<string, DayFestival>()
  for (const key of months) {
    const [year, month] = key.split('-').map(Number)
    try {
      const res = await getLunarCalendar(year, month)
      for (const cell of res.data?.data?.days || []) {
        const dayKey = `${cell.year}-${String(cell.month).padStart(2, '0')}-${String(cell.day).padStart(2, '0')}`
        next.set(dayKey, { festival: cell.festival, term: cell.term, lunar: cell.lunar })
      }
    } catch {
      // 节日只是装饰，取不到不能影响提醒本身。
    }
  }
  festivalByDay.value = next
}

// 周视图：本周一到周日。月视图：覆盖整月所需的完整周（含相邻月份补格），
// 与系统日历一致 —— 补格里的提醒也照常显示，点进去能看当天。
const range = computed(() => {
  const base = parseKey(anchor.value)
  if (isWeek.value) {
    const start = addDays(base, -weekdayIndex(base))
    return { start, end: addDays(start, 7) }
  }
  const first = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), 1))
  const start = addDays(first, -weekdayIndex(first))
  const lastOfMonth = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth() + 1, 0))
  const end = addDays(lastOfMonth, 7 - weekdayIndex(lastOfMonth))
  return { start, end }
})

const days = computed(() => {
  const byDay = new Map<string, ReminderItem[]>()
  for (const item of items.value) {
    if (!item.due_at) continue
    const key = shanghaiDay.format(new Date(item.due_at))
    const list = byDay.get(key)
    if (list) list.push(item)
    else byDay.set(key, [item])
  }
  const result = []
  const cursor = new Date(range.value.start.getTime())
  const today = todayKey()
  const anchorMonth = parseKey(anchor.value).getUTCMonth()
  while (cursor < range.value.end) {
    const key = keyOf(cursor)
    const list = byDay.get(key) || []
    list.sort((a, b) => String(a.due_at).localeCompare(String(b.due_at)))
    const info = festivalByDay.value.get(key)
    result.push({
      key,
      dayNum: cursor.getUTCDate(),
      weekdayLabel: weekdayLabels[weekdayIndex(cursor)],
      isToday: key === today,
      inMonth: cursor.getUTCMonth() === anchorMonth,
      items: list,
      festival: info?.festival || '',
      term: info?.term || '',
      lunar: info?.lunar || '',
      holiday: holidayByDay.value.get(key),
    })
    cursor.setUTCDate(cursor.getUTCDate() + 1)
  }
  return result
})

// 当前可见范围里，哪些年份不在节假日数据覆盖内（通常是次年公告尚未发布）。
const uncoveredYears = computed(() => {
  if (!holidayMinYear.value || !holidayMaxYear.value) return []
  const startYear = range.value.start.getUTCFullYear()
  const endYear = new Date(range.value.end.getTime() - 86400000).getUTCFullYear()
  const out: number[] = []
  for (let year = startYear; year <= endYear; year++) {
    if (year < holidayMinYear.value || year > holidayMaxYear.value) out.push(year)
  }
  return out
})

const totalItems = computed(() => days.value.reduce((sum, day) => sum + day.items.length, 0))
const completedCount = computed(() =>
  days.value.reduce((sum, day) => sum + day.items.filter(item => item.completed_at).length, 0))

const rangeLabel = computed(() => {
  const { start, end } = range.value
  const last = addDays(end, -1)
  if (isWeek.value) {
    const sameYear = start.getUTCFullYear() === last.getUTCFullYear()
    const left = sameYear
      ? `${start.getUTCMonth() + 1}月${start.getUTCDate()}日`
      : `${start.getUTCFullYear()}年${start.getUTCMonth() + 1}月${start.getUTCDate()}日`
    return `${left} – ${last.getUTCMonth() + 1}月${last.getUTCDate()}日`
  }
  return `${start.getUTCFullYear()}年${parseKey(anchor.value).getUTCMonth() + 1}月`
})

function timeLabel(item: ReminderItem): string {
  if (item.all_day) return '全天'
  if (!item.due_at) return ''
  return timeFmt.format(new Date(item.due_at))
}

async function load() {
  loading.value = true
  try {
    const res = await getReminders({
      view: 'calendar',
      from: keyOf(range.value.start),
      to: keyOf(range.value.end),
    })
    items.value = res.data?.data || []
    await Promise.all([loadFestivals(), loadHolidays()])
  } catch {
    items.value = []
  } finally {
    loading.value = false
  }
}

// 翻页：周视图按周（7 天）移动，月视图按月移动，锚点始终落在月初/当周周一，
// 反复翻页不会因累积偏移漂移。
function shift(direction: number) {
  const base = parseKey(anchor.value)
  if (isWeek.value) {
    anchor.value = keyOf(addDays(base, direction * 7))
    return
  }
  anchor.value = keyOf(new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth() + direction, 1)))
}

function goToday() {
  anchor.value = todayKey()
}

function openDay(day: { key: string }) {
  router.push(`/admin/day/${day.key}`)
}

// 在日视图里改完提醒回到这里时，日历要跟着更新。
function onRealtime() {
  void load()
}

watch(() => [route.name, range.value.start.getTime(), range.value.end.getTime()], load, { immediate: true })
onMounted(() => {
  window.addEventListener('reminder-realtime', onRealtime)
  window.addEventListener('reminder-realtime-resume', onRealtime)
})
onBeforeUnmount(() => {
  window.removeEventListener('reminder-realtime', onRealtime)
  window.removeEventListener('reminder-realtime-resume', onRealtime)
})
</script>

<style scoped>
.cal-nav { @apply flex h-9 w-9 items-center justify-center rounded-xl border border-border bg-surface text-lg font-bold text-foreground transition hover:bg-muted; }
.cal-today { @apply h-9 rounded-xl border border-border bg-surface px-3 text-xs font-bold text-foreground transition hover:bg-muted; }

.week-col { @apply rounded-2xl border border-border/70 bg-surface/70 p-2; }
.week-col-today { @apply border-brand-500/40 bg-brand-500/[.06]; }
.week-head { @apply flex w-full items-center gap-1.5 rounded-lg px-1 py-0.5 text-left transition hover:bg-muted/60; }
.week-name { @apply text-[10px] font-bold text-muted-foreground; }
.week-num { @apply text-base font-extrabold text-foreground; }
.week-count { @apply ml-auto rounded-full bg-brand-500/10 px-1.5 text-[10px] font-bold text-brand-600 dark:text-brand-300; }
.week-body { @apply mt-1 flex flex-col gap-1; }
.week-empty { @apply py-2 text-center text-[10px] text-muted-foreground/60; }
.week-fest { @apply mt-0.5 truncate px-1 text-[9px] font-bold leading-tight text-rose-500; }
.week-fest-term { @apply text-emerald-600 dark:text-emerald-400; }

.cal-chip { @apply flex w-full items-start gap-1 rounded-lg bg-muted/60 px-1.5 py-1 text-left transition hover:bg-brand-500/10; }
.cal-chip-time { @apply shrink-0 text-[9px] font-bold tabular-nums text-muted-foreground; }
.cal-chip-title { @apply min-w-0 flex-1 truncate text-[11px] font-semibold text-foreground; }
.cal-chip-done .cal-chip-title { @apply text-muted-foreground line-through; }

.month-wrap { @apply rounded-2xl border border-border/70 bg-surface/70 p-2 sm:p-3; }
.month-weekdays { @apply mb-1 grid grid-cols-7 gap-1 text-center text-[10px] font-bold text-muted-foreground; }
.month-grid { @apply grid grid-cols-7 gap-1; }
.month-cell { @apply relative flex min-h-16 flex-col items-start rounded-xl border border-transparent p-1.5 text-left transition hover:border-brand-500/25 hover:bg-brand-500/[.05] sm:min-h-24; }
.holiday-badge { @apply absolute right-1 top-1 rounded px-1 text-[9px] font-bold leading-4; }
.holiday-badge-inline { @apply shrink-0 rounded px-1 text-[9px] font-bold leading-4; }
.holiday-off { @apply bg-rose-500 text-white; }
.holiday-work { @apply bg-slate-400 text-white; }
.month-cell-today { @apply border-brand-500/40 bg-brand-500/[.06]; }
.month-cell-outside { @apply opacity-45; }
.month-num { @apply text-[11px] font-bold text-foreground sm:text-xs; }
.month-num-today { @apply flex h-5 min-w-5 items-center justify-center rounded-full bg-brand-500 px-1 text-white; }
.month-num-festival { @apply text-rose-500; }
.month-num-term { @apply text-emerald-600 dark:text-emerald-400; }
.month-fest { @apply mt-0.5 w-full truncate text-[8px] font-bold leading-tight text-rose-500 sm:text-[10px]; }
.month-fest-term { @apply text-emerald-600 dark:text-emerald-400; }
.month-item { @apply flex items-center gap-1 text-[10px] font-medium text-foreground; }
.month-item-done { @apply text-muted-foreground line-through; }
.month-dot { @apply h-1 w-1 shrink-0 rounded-full bg-brand-500; }
.month-item-done .month-dot { @apply bg-muted-foreground; }
.month-more { @apply pl-2 text-[9px] font-bold text-muted-foreground; }
.month-pip { @apply h-1 w-1 rounded-full bg-brand-500; }
.month-pip-done { @apply bg-muted-foreground; }
</style>
