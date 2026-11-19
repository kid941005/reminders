<template>
  <div class="time-wheel" role="group" aria-label="选择时间">
    <div class="wheel-highlight" aria-hidden="true"></div>

    <div ref="periodEl" class="wheel-col wheel-period" @scroll.passive="onScroll('period')">
      <div class="wheel-spacer" aria-hidden="true"></div>
      <button
        v-for="(period, i) in periods"
        :key="period.key"
        type="button"
        class="wheel-item"
        :class="{ active: i === periodHighlight }"
        :aria-pressed="i === periodHighlight"
        @click="selectPeriod(i)"
      >{{ period.label }}</button>
      <div class="wheel-spacer" aria-hidden="true"></div>
    </div>

    <div ref="hourEl" class="wheel-col" @scroll.passive="onScroll('hour')">
      <div class="wheel-spacer" aria-hidden="true"></div>
      <button
        v-for="(hour, i) in hourValues"
        :key="hour"
        type="button"
        class="wheel-item"
        :class="{ active: i === hourHighlight }"
        :aria-pressed="i === hourHighlight"
        @click="selectHour(i)"
      >{{ hourLabel(hour) }}</button>
      <div class="wheel-spacer" aria-hidden="true"></div>
    </div>

    <div ref="minuteEl" class="wheel-col" @scroll.passive="onScroll('minute')">
      <div class="wheel-spacer" aria-hidden="true"></div>
      <button
        v-for="(minute, i) in minuteValues"
        :key="minute"
        type="button"
        class="wheel-item"
        :class="{ active: i === minuteHighlight }"
        :aria-pressed="i === minuteHighlight"
        @click="selectMinute(i)"
      >{{ pad2(minute) }}</button>
      <div class="wheel-spacer" aria-hidden="true"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

/**
 * Apple 闹钟式三列时间滚轮：时段 / 小时 / 分钟。
 *
 * 时段列把 24 小时分成中文习惯的 6 段，小时列只显示当前时段内的值，
 * 这样「凌晨 12」和「中午 12」不会产生歧义。滚动停止后才向上抛事件，
 * 避免滚动过程中每一帧都触发父组件重渲染。
 */
interface Period {
  key: string
  label: string
  hours: number[]
}

const periods: Period[] = [
  { key: 'dawn', label: '凌晨', hours: [0, 1, 2, 3, 4, 5] },
  { key: 'morning', label: '早上', hours: [6, 7, 8] },
  { key: 'forenoon', label: '上午', hours: [9, 10, 11] },
  { key: 'noon', label: '中午', hours: [12] },
  { key: 'afternoon', label: '下午', hours: [13, 14, 15, 16, 17] },
  { key: 'evening', label: '晚上', hours: [18, 19, 20, 21, 22, 23] },
]

const props = withDefaults(defineProps<{
  hour: number
  minute: number
  /** 今天可选的最早小时（含）；不传表示没有限制 */
  minHour?: number
  /** minHour 这一小时内可选的最早分钟（含） */
  minMinute?: number
}>(), { minHour: undefined, minMinute: undefined })

const emit = defineEmits<{
  (e: 'change', value: { hour: number; minute: number }): void
}>()

const ITEM_HEIGHT = 40
const minuteValues = Array.from({ length: 60 }, (_, i) => i)

const periodEl = ref<HTMLElement | null>(null)
const hourEl = ref<HTMLElement | null>(null)
const minuteEl = ref<HTMLElement | null>(null)

// 已提交的下标决定数据；Highlight 是滚动中的视觉高亮，两者分开可以
// 让用户滚到下一行时立刻看到反馈，但不会在滚动途中反复触发保存。
const periodIndex = ref(0)
const hourIndex = ref(0)
const minuteIndex = ref(0)
const periodHighlight = ref(0)
const hourHighlight = ref(0)
const minuteHighlight = ref(0)

const hourValues = computed(() => periods[periodIndex.value]?.hours || periods[0].hours)
let applying = false
const timers: Partial<Record<'period' | 'hour' | 'minute', number>> = {}

function pad2(value: number) {
  return String(value).padStart(2, '0')
}
function hourLabel(hour: number) {
  const twelve = hour % 12
  return String(twelve === 0 ? 12 : twelve)
}
function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value))
}
function scrollToIndex(el: HTMLElement | null, index: number) {
  if (!el) return
  const top = index * ITEM_HEIGHT
  if (Math.abs(el.scrollTop - top) < 1) return
  applying = true
  el.scrollTop = top
  // 等浏览器把这次程序化滚动产生的 scroll 事件派发完再恢复监听。
  requestAnimationFrame(() => requestAnimationFrame(() => { applying = false }))
}

function normalize(hour: number, minute: number) {
  if (props.minHour === undefined) return { hour, minute }
  if (hour < props.minHour) return { hour: props.minHour, minute: props.minMinute ?? 0 }
  if (hour === props.minHour && minute < (props.minMinute ?? 0)) {
    return { hour, minute: props.minMinute ?? 0 }
  }
  return { hour, minute }
}

function emitTime(hour: number, minute: number) {
  const next = normalize(hour, minute)
  if (next.hour === props.hour && next.minute === props.minute) return
  emit('change', next)
}

function syncFromProps() {
  const nextPeriod = Math.max(0, periods.findIndex(period => period.hours.includes(props.hour)))
  periodIndex.value = nextPeriod
  periodHighlight.value = nextPeriod
  const nextHour = Math.max(0, hourValues.value.indexOf(props.hour))
  hourIndex.value = nextHour
  hourHighlight.value = nextHour
  const nextMinute = clamp(props.minute, 0, 59)
  minuteIndex.value = nextMinute
  minuteHighlight.value = nextMinute
  void nextTick(() => {
    scrollToIndex(periodEl.value, nextPeriod)
    scrollToIndex(hourEl.value, nextHour)
    scrollToIndex(minuteEl.value, nextMinute)
  })
}

function commitPeriod(index: number) {
  const period = periods[index]
  if (!period) return
  periodIndex.value = index
  periodHighlight.value = index
  // 换时段时保留最接近的绝对小时，避免从「晚上 8 点」切到「下午」后
  // 直接跳到下午 1 点这种突兀行为。
  const nearest = period.hours.reduce((best, hour) =>
    Math.abs(hour - props.hour) < Math.abs(best - props.hour) ? hour : best, period.hours[0])
  const nextHour = Math.max(0, period.hours.indexOf(nearest))
  hourIndex.value = nextHour
  hourHighlight.value = nextHour
  void nextTick(() => scrollToIndex(hourEl.value, nextHour))
  emitTime(period.hours[nextHour], minuteIndex.value)
}

function commitHour(index: number) {
  const hour = hourValues.value[index]
  if (hour === undefined) return
  hourIndex.value = index
  hourHighlight.value = index
  emitTime(hour, minuteIndex.value)
}

function commitMinute(index: number) {
  const minute = minuteValues[index]
  if (minute === undefined) return
  minuteIndex.value = index
  minuteHighlight.value = index
  emitTime(hourValues.value[hourIndex.value] ?? props.hour, minute)
}

function onScroll(kind: 'period' | 'hour' | 'minute') {
  if (applying) return
  const el = kind === 'period' ? periodEl.value : kind === 'hour' ? hourEl.value : minuteEl.value
  if (!el) return
  const max = kind === 'period' ? periods.length - 1 : kind === 'hour' ? hourValues.value.length - 1 : 59
  const index = clamp(Math.round(el.scrollTop / ITEM_HEIGHT), 0, max)
  if (kind === 'period') periodHighlight.value = index
  else if (kind === 'hour') hourHighlight.value = index
  else minuteHighlight.value = index
  if (timers[kind] !== undefined) window.clearTimeout(timers[kind])
  timers[kind] = window.setTimeout(() => {
    timers[kind] = undefined
    if (kind === 'period') commitPeriod(index)
    else if (kind === 'hour') commitHour(index)
    else commitMinute(index)
  }, 120)
}

function selectPeriod(index: number) {
  scrollToIndex(periodEl.value, index)
  periodHighlight.value = index
  commitPeriod(index)
}
function selectHour(index: number) {
  scrollToIndex(hourEl.value, index)
  hourHighlight.value = index
  commitHour(index)
}
function selectMinute(index: number) {
  scrollToIndex(minuteEl.value, index)
  minuteHighlight.value = index
  commitMinute(index)
}

watch(() => [props.hour, props.minute], syncFromProps)
watch(() => [props.minHour, props.minMinute], () => {
  const next = normalize(props.hour, props.minute)
  if (next.hour !== props.hour || next.minute !== props.minute) emit('change', next)
})
onMounted(syncFromProps)
onBeforeUnmount(() => {
  for (const timer of Object.values(timers)) {
    if (timer !== undefined) window.clearTimeout(timer)
  }
})
</script>

<style scoped>
.time-wheel {
  @apply relative grid h-[200px] grid-cols-[1.15fr_1fr_1fr] gap-1 overflow-hidden rounded-2xl bg-muted/45 px-2;
}
.wheel-highlight {
  @apply pointer-events-none absolute left-2 right-2 top-[80px] h-10 rounded-xl bg-surface shadow-sm ring-1 ring-border/70;
}
.wheel-col {
  @apply relative z-[1] h-full overflow-y-auto overscroll-contain;
  scroll-snap-type: y mandatory;
  scrollbar-width: none;
}
.wheel-col::-webkit-scrollbar {
  display: none;
}
.wheel-spacer {
  height: 80px;
}
.wheel-item {
  @apply flex h-10 w-full shrink-0 items-center justify-center whitespace-nowrap text-[15px] font-semibold text-muted-foreground/55 transition-colors;
  scroll-snap-align: center;
}
.wheel-item.active {
  @apply text-foreground;
}
.wheel-period .wheel-item {
  @apply text-[13px];
}
</style>
