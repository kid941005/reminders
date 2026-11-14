<template>
  <div class="page-container animate-fade-in">
    <PageHeader title="偏好设置" description="管理你的个人功能配置" />

    <div v-if="errorMsg" class="surface rounded-2xl px-5 py-3 text-sm text-destructive">
      {{ errorMsg }}
    </div>

    <div v-if="loading" class="space-y-3">
      <div v-for="i in 4" :key="i" class="h-20 rounded-xl bg-muted animate-pulse"></div>
    </div>

    <ConfigGroupList
      v-else
      ref="listRef"
      :items="items"
      @save="handleSave"
    />

    <!-- 日历订阅：把提醒喂给系统日历 / Google Calendar，弥补没有移动客户端的短板 -->
    <div class="surface mt-4 rounded-2xl p-4 sm:mt-6 sm:p-6">
      <div class="flex items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-sky-500/12 text-sky-500">
          <svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <rect x="4" y="5" width="16" height="15" rx="3" stroke-width="1.8" />
            <path d="M8 3v4m8-4v4M4 10h16" stroke-width="1.8" stroke-linecap="round" />
            <path d="M9 14.5h6" stroke-width="1.8" stroke-linecap="round" />
          </svg>
        </div>
        <div class="min-w-0 flex-1">
          <h3 class="text-sm font-bold text-foreground">日历订阅（ICS）</h3>
          <p class="mt-1 text-sm leading-relaxed text-muted-foreground">
            把这个地址加到系统日历、Google Calendar 或 Outlook，未完成的提醒会自动同步过去。
            订阅只读，日历里的改动不会回写。
          </p>
        </div>
      </div>

      <div class="mt-4 flex flex-col gap-2 sm:flex-row">
        <input
          :value="feedUrl"
          readonly
          class="compact-input flex-1 font-mono text-[11px]"
          :placeholder="feedLoading ? '正在生成订阅地址…' : '订阅地址不可用'"
          @focus="selectFeedInput"
        />
        <button type="button" class="btn-ghost whitespace-nowrap" :disabled="!feedUrl || feedBusy" @click="copyFeed">
          {{ feedBusy ? '处理中…' : '复制地址' }}
        </button>
        <button type="button" class="btn-ghost whitespace-nowrap text-rose-500" :disabled="!feedUrl || feedBusy" @click="askRotateFeed">
          重置地址
        </button>
      </div>

      <p v-if="feedMsg" class="mt-2 text-xs font-semibold" :class="feedMsgType === 'error' ? 'text-rose-500' : 'text-emerald-500'">
        {{ feedMsg }}
      </p>
      <p class="mt-2 text-[11px] leading-5 text-muted-foreground">
        地址里带有访问令牌，拿到它就能读取你的提醒，请勿公开分享。重置后旧地址立即失效，需要在日历端重新添加。
      </p>
    </div>

    <!-- 钉钉日程：CalDAV 单向导入，钉钉里的日程会落到「钉钉日程」清单 -->
    <div class="surface mt-4 rounded-2xl p-4 sm:mt-6 sm:p-6">
      <div class="flex items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-indigo-500/12 text-indigo-500">
          <svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path d="M12 3v12m0 0 4-4m-4 4-4-4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </div>
        <div class="min-w-0 flex-1">
          <h3 class="text-sm font-bold text-foreground">钉钉日程（单向导入）</h3>
          <p class="mt-1 text-sm leading-relaxed text-muted-foreground">
            在钉钉桌面端「日历设置 → CalDAV 同步」生成账号与密码，填在这里即可把日程导入到
            「钉钉日程」清单。只读：本地改动不会写回钉钉，每次同步以钉钉为准。
          </p>
        </div>
      </div>

      <div class="mt-4 grid gap-2 sm:grid-cols-3">
        <input v-model="syncForm.server" class="compact-input" placeholder="服务器，如 calendar.dingtalk.com" />
        <input v-model="syncForm.username" class="compact-input" placeholder="账号" autocomplete="off" />
        <input
          v-model="syncForm.password"
          class="compact-input"
          type="password"
          autocomplete="new-password"
          :placeholder="syncCfg ? '留空表示不修改' : '密码'"
        />
      </div>

      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="btn-brand" :disabled="syncBusy" @click="saveSync">
          {{ syncBusy ? '处理中…' : syncCfg ? '保存并同步' : '保存并开始同步' }}
        </button>
        <button type="button" class="btn-ghost" :disabled="syncBusy || !syncCfg" @click="runSync()">立即同步</button>
        <button type="button" class="btn-ghost text-rose-500" :disabled="syncBusy || !syncCfg" @click="disableSync">关闭同步</button>
      </div>

      <p v-if="syncMsg" class="mt-2 text-xs font-semibold" :class="syncMsgType === 'error' ? 'text-rose-500' : 'text-emerald-500'">
        {{ syncMsg }}
      </p>
      <p v-if="syncCfg" class="mt-2 text-[11px] leading-5 text-muted-foreground">
        清单「{{ syncCfg.list_name }}」· 上次同步 {{ syncCfg.last_sync_at ? new Date(syncCfg.last_sync_at).toLocaleString() : '尚未运行' }} ·
        上次导入 {{ syncCfg.last_imported }} 条 ·
        状态 {{ syncCfg.last_status === 'ok' ? '正常' : syncCfg.last_status === 'error' ? '失败' : '未知' }}<span v-if="syncCfg.last_error">：{{ syncCfg.last_error }}</span>
      </p>
      <p class="mt-2 text-[11px] leading-5 text-muted-foreground">
        密码用本地密钥加密保存，不会回传前端。同步每 30 分钟运行一次；窗口默认前 7 天、后 180 天。
      </p>
    </div>

    <!-- 天气提醒：管理员配好和风天气 API，用户选城市和时间，每天上午推送摘要 -->
    <div class="surface mt-4 rounded-2xl p-4 sm:mt-6 sm:p-6">
      <div class="flex items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-sky-500/12 text-sky-500">
          <svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path d="M7 18a4 4 0 0 1-.5-7.97A5.5 5.5 0 0 1 17.2 9.2 4.4 4.4 0 0 1 17 18H7Z" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M9 21h6" stroke-width="1.8" stroke-linecap="round" />
          </svg>
        </div>
        <div class="min-w-0 flex-1">
          <h3 class="text-sm font-bold text-foreground">天气提醒</h3>
          <p class="mt-1 text-sm leading-relaxed text-muted-foreground">
            每天早上推送当天天气，晚上推送次日天气；每条只保留对应时段的预报和预警。
            天气数据来自和风天气，需要管理员先配置 API。
          </p>
        </div>
      </div>

      <div v-if="authStore.isAdmin" class="mt-4 rounded-xl border border-border/60 p-3">
        <p class="text-xs font-semibold text-foreground">管理员配置</p>
        <div class="mt-2 grid gap-2 sm:grid-cols-2">
          <input v-model="weatherAdmin.host" class="compact-input" placeholder="API Host，如 abcxyz.qweatherapi.com" autocomplete="off" />
          <input
            v-model="weatherAdmin.api_key"
            class="compact-input"
            type="password"
            autocomplete="new-password"
            :placeholder="weatherAdminConfigured ? 'API Key 已配置，留空表示不修改' : '和风天气 API Key'"
          />
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button type="button" class="btn-ghost" :disabled="weatherAdminBusy" @click="saveWeatherAdminConfig">
            {{ weatherAdminBusy ? '验证中…' : '保存 API 配置' }}
          </button>
          <span v-if="weatherAdminConfigured" class="text-[11px] text-emerald-500">
            已配置<span v-if="weatherAdminHost"> · {{ weatherAdminHost }}</span>
          </span>
          <span v-else class="text-[11px] text-amber-500">尚未配置，天气提醒不可用</span>
        </div>
        <p class="mt-2 text-[11px] leading-5 text-muted-foreground">
          API Key 加密保存在服务端，只用于调用和风天气，不会回传到浏览器。
        </p>
      </div>

      <div v-if="weatherCfg && !weatherCfg.admin_configured" class="mt-3 rounded-xl bg-amber-400/10 px-3 py-2 text-xs text-amber-600">
        管理员尚未配置和风天气 API，暂时无法保存天气提醒。
      </div>

      <div class="mt-4 grid gap-2 sm:grid-cols-[1fr_auto]">
        <input
          v-model="weatherCityQuery"
          class="compact-input"
          placeholder="搜索城市，如 北京 / 杭州"
          @keyup.enter="searchWeatherCity"
        />
        <button type="button" class="btn-ghost whitespace-nowrap" :disabled="weatherCityBusy" @click="searchWeatherCity">
          {{ weatherCityBusy ? '搜索中…' : '搜索城市' }}
        </button>
      </div>
      <div v-if="weatherCityResults.length" class="mt-2 overflow-hidden rounded-xl border border-border/60">
        <button
          v-for="loc in weatherCityResults"
          :key="loc.id"
          type="button"
          class="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-xs hover:bg-muted"
          @click="pickWeatherLocation(loc)"
        >
          <span class="font-semibold text-foreground">{{ loc.name }}</span>
          <span class="text-muted-foreground">{{ loc.adm1 }} {{ loc.adm2 }} · {{ loc.lat }}, {{ loc.lon }}</span>
        </button>
      </div>

      <div class="mt-3 grid gap-2 sm:grid-cols-4">
        <input v-model="weatherForm.location_name" class="compact-input sm:col-span-2" placeholder="地点名称，如 杭州市" />
        <input v-model="weatherForm.latitude" class="compact-input" inputmode="decimal" placeholder="纬度，如 30.27" />
        <input v-model="weatherForm.longitude" class="compact-input" inputmode="decimal" placeholder="经度，如 120.15" />
      </div>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <label class="flex items-center gap-2 text-xs text-muted-foreground">
          早间推送
          <input v-model="weatherForm.morning_time" type="time" class="compact-input w-32" />
        </label>
        <label class="flex items-center gap-2 text-xs text-muted-foreground">
          晚间推送
          <input v-model="weatherForm.evening_time" type="time" class="compact-input w-32" />
        </label>
        <label class="flex items-center gap-2 text-xs text-muted-foreground">
          <input v-model="weatherForm.enabled" type="checkbox" class="h-4 w-4 accent-sky-500" />
          启用每日推送
        </label>
      </div>
      <p class="mt-1 text-[11px] leading-5 text-muted-foreground">
        早间推送当天天气，晚间推送次日天气；两个时间不能相同，且晚间应晚于早间。
      </p>

      <div class="mt-3">
        <p class="text-xs font-semibold text-foreground">通知渠道</p>
        <div class="mt-2 flex flex-wrap gap-2">
          <label
            v-for="ch in weatherChannelOptions"
            :key="ch.channel"
            class="flex items-center gap-2 rounded-lg border border-border/60 px-2.5 py-1.5 text-xs"

          >
            <input
              v-model="weatherForm.channels"
              type="checkbox"
              class="h-3.5 w-3.5 accent-sky-500"
              :value="ch.channel"
            />
            {{ ch.label }}
          </label>
        </div>
        <p class="mt-1 text-[11px] leading-5 text-muted-foreground">
          站内通知默认不开启；所有渠道都需要先勾选，其他渠道还需在「通知渠道」里完成绑定。
        </p>
      </div>

      <div class="mt-3 flex flex-wrap items-center gap-2">
        <span class="text-xs text-muted-foreground">预览 / 推送时段</span>
        <button
          type="button"
          class="btn-ghost"
          :class="weatherSlot === 'morning' ? 'ring-2 ring-sky-400/60' : ''"
          @click="weatherSlot = 'morning'"
        >
          早间 · 今天
        </button>
        <button
          type="button"
          class="btn-ghost"
          :class="weatherSlot === 'evening' ? 'ring-2 ring-sky-400/60' : ''"
          @click="weatherSlot = 'evening'"
        >
          晚间 · 次日
        </button>
      </div>

      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="btn-brand" :disabled="weatherBusy || weatherCfg?.admin_configured === false" @click="saveWeather">
          {{ weatherBusy ? '处理中…' : weatherCfg?.configured ? '保存天气设置' : '开启天气提醒' }}
        </button>
        <button type="button" class="btn-ghost" :disabled="weatherBusy || !weatherCfg?.configured" @click="previewWeatherNow">
          预览
        </button>
        <button type="button" class="btn-ghost" :disabled="weatherBusy || !weatherCfg?.configured" @click="runWeatherNow">
          立即推送
        </button>
        <button type="button" class="btn-ghost text-rose-500" :disabled="weatherBusy || !weatherCfg?.configured" @click="disableWeather">
          关闭天气提醒
        </button>
      </div>

      <p v-if="weatherMsg" class="mt-2 text-xs font-semibold" :class="weatherMsgType === 'error' ? 'text-rose-500' : 'text-emerald-500'">
        {{ weatherMsg }}
      </p>
      <p v-if="weatherCfg?.configured" class="mt-2 text-[11px] leading-5 text-muted-foreground">
        {{ weatherCfg.location_name }} · 每天 {{ weatherCfg.morning_time }} / {{ weatherCfg.evening_time }} ·
        上次推送 {{ weatherCfg.last_push_at ? new Date(weatherCfg.last_push_at).toLocaleString() : '尚未运行' }}<span v-if="weatherCfg.last_slot">（{{ weatherCfg.last_slot === 'evening' ? '晚间' : '早间' }}）</span> ·
        状态 {{ weatherCfg.last_status === 'ok' ? '正常' : weatherCfg.last_status === 'partial' ? '部分渠道失败' : weatherCfg.last_status === 'error' ? '失败' : '未知' }}<span v-if="weatherCfg.last_error">：{{ weatherCfg.last_error }}</span>
      </p>
      <pre v-if="weatherPreview" class="mt-3 max-h-64 overflow-auto whitespace-pre-wrap rounded-xl bg-muted/60 p-3 text-xs leading-6 text-foreground">{{ weatherPreview.title }}

{{ weatherPreview.body }}</pre>
    </div>

    <!-- 支持作者（仅管理员可见，赞赏完全自愿）；本发行版默认关闭，见 stores/support.ts -->
    <div v-if="authStore.isAdmin && supportStore.enabled" class="surface rounded-2xl p-4 sm:p-6">
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div class="flex items-start gap-3">
          <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-amber-400/15 text-amber-500">
            <svg class="h-5 w-5" fill="currentColor" viewBox="0 0 24 24"><path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z"/></svg>
          </div>
          <div>
            <h3 class="text-sm font-bold text-foreground">支持作者</h3>
            <p class="mt-1 text-sm leading-relaxed text-muted-foreground">
              这个应用免费、无广告，数据完全保存在你自己的设备上。
              如果它帮到了你，欢迎请作者喝杯咖啡——金额随意，1 元也是心意；
              <strong class="text-foreground">不赞赏不影响任何功能</strong>。
            </p>
            <p v-if="supportStore.donateSupported" class="mt-2 text-xs text-emerald-500">
              ❤ 感谢你的支持，这也太暖了{{ supportStore.supportedVersion ? `（版本 ${supportStore.supportedVersion}）` : '' }}
            </p>
          </div>
        </div>
        <button class="btn-brand flex-1 sm:flex-none" @click="supportStore.open()">
          {{ supportStore.donateSupported ? '再次赞赏' : '去赞赏' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import ConfigGroupList from '../components/ConfigGroupList.vue'
import { getUserConfigMeta, updateConfig } from '../api/config'
import { getCalendarFeed, resetCalendarFeed } from '../api/countdown'
import { deleteCalendarSync, getCalendarSync, getChannelStatuses, runCalendarSync, saveCalendarSync } from '../api/reminder'
import type { CalendarSyncConfig, ChannelStatus, ReminderChannel } from '../api/reminder'
import {
  deleteWeatherConfig,
  getWeatherAdmin,
  getWeatherConfig,
  lookupWeatherLocations,
  previewWeather,
  runWeather,
  saveWeatherAdmin,
  saveWeatherConfig,
} from '../api/weather'
import type { WeatherConfig, WeatherLocation, WeatherMessage, WeatherSlot } from '../api/weather'
import type { ConfigMeta } from '../api/config'
import { useThemeStore } from '../stores/theme'
import { useAuthStore } from '../stores/auth'
import { useSupportStore } from '../stores/support'

const authStore = useAuthStore()
const supportStore = useSupportStore()
const items = ref<ConfigMeta[]>([])
const loading = ref(false)
const errorMsg = ref('')
const listRef = ref<InstanceType<typeof ConfigGroupList> | null>(null)

// 日历订阅地址：后端的令牌是幂等的，重复打开设置页不会让已订阅的日历失效。
const feedUrl = ref('')
const feedLoading = ref(false)
const feedBusy = ref(false)
const feedMsg = ref('')
const feedMsgType = ref<'success' | 'error'>('success')

// 钉钉日程同步：password 只在保存时提交，读取接口不返回它。
const syncCfg = ref<CalendarSyncConfig | null>(null)
const syncForm = reactive({ server: '', username: '', password: '' })
const syncBusy = ref(false)
const syncMsg = ref('')
const syncMsgType = ref<'success' | 'error'>('success')

// 天气提醒：管理员保存和风天气的 host/key，用户保存城市、时间与渠道。
const weatherCfg = ref<WeatherConfig | null>(null)
const weatherForm = reactive({
  location_name: '',
  latitude: '',
  longitude: '',
  morning_time: '08:00',
  evening_time: '20:00',
  channels: [] as ReminderChannel[],
  enabled: true,
})
const weatherAdmin = reactive({ host: '', api_key: '' })
const weatherAdminConfigured = ref(false)
const weatherAdminHost = ref('')
const weatherAdminBusy = ref(false)
const weatherBusy = ref(false)
const weatherMsg = ref('')
const weatherMsgType = ref<'success' | 'error'>('success')
const weatherCityQuery = ref('')
const weatherCityResults = ref<WeatherLocation[]>([])
const weatherCityBusy = ref(false)
const weatherPreview = ref<WeatherMessage | null>(null)
const weatherSlot = ref<WeatherSlot>('morning')
const weatherChannelStatuses = ref<ChannelStatus[]>([])
const weatherChannelOptions = computed(() => {
  const usable = weatherChannelStatuses.value.filter(
    (ch) => ch.channel === 'inapp' || (ch.configured && ch.bound),
  )
  // 渠道列表加载失败时至少保证站内通知可选。
  return usable.length ? usable : [{ channel: 'inapp', label: '站内通知' } as ChannelStatus]
})

onMounted(() => {
  loadMeta()
  loadFeed()
  loadCalendarSync()
  loadWeather()
  loadWeatherAdmin()
  loadChannelStatuses()
})

async function loadCalendarSync() {
  try {
    const res = await getCalendarSync()
    const data = (res.data?.data || null) as CalendarSyncConfig | null
    syncCfg.value = data
    if (data) {
      syncForm.server = data.server || ''
      syncForm.username = data.username || ''
    } else {
      // 未配置时给一个默认值，减少一次输入。
      syncForm.server = 'calendar.dingtalk.com'
    }
    syncForm.password = ''
  } catch {
    // 未配置时后端返回 null，读取失败按未配置处理，不打断设置页。
    syncCfg.value = null
  }
}

function notifySync(message: string, type: 'success' | 'error' = 'success') {
  syncMsg.value = message
  syncMsgType.value = type
  window.setTimeout(() => { syncMsg.value = '' }, 4000)
}

async function saveSync() {
  if (syncBusy.value) return
  if (!syncForm.server.trim() || !syncForm.username.trim()) {
    notifySync('请填写服务器与账号', 'error')
    return
  }
  if (!syncCfg.value && !syncForm.password) {
    notifySync('首次配置请填写密码', 'error')
    return
  }
  syncBusy.value = true
  try {
    const res = await saveCalendarSync({
      server: syncForm.server.trim(),
      username: syncForm.username.trim(),
      password: syncForm.password || undefined,
      enabled: true,
    })
    syncCfg.value = (res.data?.data || null) as CalendarSyncConfig | null
    syncForm.password = ''
    notifySync('已保存，正在同步…')
    await runSync(true)
  } catch (err: any) {
    notifySync(err.response?.data?.message || '保存失败', 'error')
  } finally {
    syncBusy.value = false
  }
}

async function runSync(silent = false) {
  if (!syncCfg.value) {
    if (!silent) notifySync('请先保存配置', 'error')
    return
  }
  syncBusy.value = true
  try {
    const res = await runCalendarSync()
    syncCfg.value = (res.data?.data?.config || syncCfg.value) as CalendarSyncConfig
    notifySync(`已同步 ${res.data?.data?.imported ?? 0} 条`)
  } catch (err: any) {
    notifySync(err.response?.data?.message || '同步失败', 'error')
  } finally {
    syncBusy.value = false
  }
}

async function disableSync() {
  if (syncBusy.value) return
  const ok = window.confirm('关闭同步后不再拉取钉钉日程，已导入的条目会保留。确定关闭吗？')
  if (!ok) return
  syncBusy.value = true
  try {
    await deleteCalendarSync()
    syncCfg.value = null
    notifySync('已关闭同步')
  } catch (err: any) {
    notifySync(err.response?.data?.message || '关闭失败', 'error')
  } finally {
    syncBusy.value = false
  }
}

async function loadFeed() {
  feedLoading.value = true
  try {
    const res = await getCalendarFeed()
    feedUrl.value = res.data?.data?.url || ''
  } catch {
    feedUrl.value = ''
  } finally {
    feedLoading.value = false
  }
}

function selectFeedInput(event: Event) {
  (event.target as HTMLInputElement).select()
}

function notifyFeed(message: string, type: 'success' | 'error' = 'success') {
  feedMsg.value = message
  feedMsgType.value = type
  window.setTimeout(() => { feedMsg.value = '' }, 3000)
}

async function copyFeed() {
  try {
    // 非 HTTPS 环境下 clipboard 可能不可用，退回手动选中，让用户自己复制。
    await navigator.clipboard.writeText(feedUrl.value)
    notifyFeed('地址已复制')
  } catch {
    notifyFeed('复制失败，请手动选中地址后复制', 'error')
  }
}

async function askRotateFeed() {
  if (feedBusy.value) return
  // 重置会让已经添加的日历全部失效，必须二次确认。
  const ok = window.confirm('重置后旧地址立即失效，已添加到日历的订阅需要重新添加。确定重置吗？')
  if (!ok) return
  feedBusy.value = true
  try {
    const res = await resetCalendarFeed()
    feedUrl.value = res.data?.data?.url || ''
    notifyFeed('已重置，请用新地址重新订阅')
  } catch (err: any) {
    notifyFeed(err.response?.data?.message || '重置失败', 'error')
  } finally {
    feedBusy.value = false
  }
}

async function loadWeather() {
  try {
    const res = await getWeatherConfig()
    const data = (res.data?.data || null) as WeatherConfig | null
    weatherCfg.value = data
    if (data?.configured) {
      weatherForm.location_name = data.location_name || ''
      weatherForm.latitude = String(data.latitude ?? '')
      weatherForm.longitude = String(data.longitude ?? '')
      weatherForm.morning_time = data.morning_time || '08:00'
      weatherForm.evening_time = data.evening_time || '20:00'
      weatherForm.channels = [...(data.channels || [])] as ReminderChannel[]
      weatherForm.enabled = data.enabled
    } else {
      weatherForm.channels = []
      weatherForm.enabled = true
    }
  } catch {
    weatherCfg.value = null
  }
}

async function loadWeatherAdmin() {
  if (!authStore.isAdmin) return
  try {
    const res = await getWeatherAdmin()
    const data = res.data?.data || {}
    weatherAdminConfigured.value = Boolean(data.configured)
    weatherAdminHost.value = data.host || ''
    if (!weatherAdmin.host && data.host) weatherAdmin.host = data.host
  } catch {
    weatherAdminConfigured.value = false
  }
}

async function loadChannelStatuses() {
  try {
    const res = await getChannelStatuses()
    const data = res.data?.data
    weatherChannelStatuses.value = Array.isArray(data) ? data : []
  } catch {
    weatherChannelStatuses.value = []
  }
}

function notifyWeather(message: string, type: 'success' | 'error' = 'success') {
  weatherMsg.value = message
  weatherMsgType.value = type
  window.setTimeout(() => { weatherMsg.value = '' }, 4000)
}

async function saveWeatherAdminConfig() {
  if (weatherAdminBusy.value) return
  if (!weatherAdmin.host.trim()) {
    notifyWeather('请填写和风天气 API Host', 'error')
    return
  }
  weatherAdminBusy.value = true
  try {
    const res = await saveWeatherAdmin({
      host: weatherAdmin.host.trim(),
      api_key: weatherAdmin.api_key.trim() || undefined,
    })
    const data = res.data?.data || {}
    weatherAdminConfigured.value = Boolean(data.configured)
    weatherAdminHost.value = data.host || ''
    weatherAdmin.host = data.host || weatherAdmin.host
    weatherAdmin.api_key = ''
    notifyWeather('API 配置已保存并通过验证')
    await loadWeather()
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || 'API 配置验证失败', 'error')
  } finally {
    weatherAdminBusy.value = false
  }
}

async function searchWeatherCity() {
  const q = weatherCityQuery.value.trim()
  if (!q) {
    notifyWeather('请输入城市名称', 'error')
    return
  }
  weatherCityBusy.value = true
  try {
    const res = await lookupWeatherLocations(q)
    const data = res.data?.data
    weatherCityResults.value = Array.isArray(data) ? data : []
    if (!weatherCityResults.value.length) notifyWeather('没有找到匹配的城市', 'error')
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || '城市搜索失败', 'error')
  } finally {
    weatherCityBusy.value = false
  }
}

function pickWeatherLocation(loc: WeatherLocation) {
  weatherForm.location_name = loc.name
  weatherForm.latitude = loc.lat
  weatherForm.longitude = loc.lon
  weatherCityResults.value = []
}

async function saveWeather() {
  if (weatherBusy.value) return
  const name = weatherForm.location_name.trim()
  if (!name) {
    notifyWeather('请填写地点名称', 'error')
    return
  }
  const latitude = Number(weatherForm.latitude)
  const longitude = Number(weatherForm.longitude)
  if (!Number.isFinite(latitude) || latitude < -90 || latitude > 90) {
    notifyWeather('纬度需在 -90 到 90 之间', 'error')
    return
  }
  if (!Number.isFinite(longitude) || longitude < -180 || longitude > 180) {
    notifyWeather('经度需在 -180 到 180 之间', 'error')
    return
  }
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(weatherForm.morning_time)) {
    notifyWeather('早间推送时间格式应为 HH:MM', 'error')
    return
  }
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(weatherForm.evening_time)) {
    notifyWeather('晚间推送时间格式应为 HH:MM', 'error')
    return
  }
  if (weatherForm.morning_time === weatherForm.evening_time) {
    notifyWeather('早间和晚间推送时间不能相同', 'error')
    return
  }
  if (weatherForm.morning_time > weatherForm.evening_time) {
    notifyWeather('晚间推送时间应晚于早间推送时间', 'error')
    return
  }
  const channels = Array.from(new Set(weatherForm.channels)) as ReminderChannel[]
  if (weatherForm.enabled && channels.length === 0) {
    notifyWeather('请至少选择一个天气通知渠道', 'error')
    return
  }
  weatherBusy.value = true
  try {
    const res = await saveWeatherConfig({
      location_name: name,
      latitude,
      longitude,
      morning_time: weatherForm.morning_time,
      evening_time: weatherForm.evening_time,
      channels,
      enabled: weatherForm.enabled,
    })
    const data = (res.data?.data || null) as WeatherConfig | null
    if (data) {
      weatherCfg.value = data
      weatherForm.latitude = String(data.latitude ?? latitude)
      weatherForm.longitude = String(data.longitude ?? longitude)
    }
    notifyWeather('天气提醒已保存')
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || '保存失败', 'error')
  } finally {
    weatherBusy.value = false
  }
}

async function previewWeatherNow() {
  if (weatherBusy.value) return
  weatherBusy.value = true
  try {
    const res = await previewWeather(weatherSlot.value)
    const data = res.data?.data || {}
    weatherPreview.value = (data.message || null) as WeatherMessage | null
    notifyWeather(weatherSlot.value === 'morning' ? '已生成今日预览，未发送' : '已生成明日预览，未发送')
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || '预览失败', 'error')
  } finally {
    weatherBusy.value = false
  }
}

async function runWeatherNow() {
  if (weatherBusy.value) return
  weatherBusy.value = true
  try {
    const res = await runWeather(weatherSlot.value)
    const data = res.data?.data || {}
    if (data.config) weatherCfg.value = data.config as WeatherConfig
    if (data.message) weatherPreview.value = data.message as WeatherMessage
    notifyWeather(weatherSlot.value === 'morning' ? '已推送今日天气' : '已推送明日天气')
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || '推送失败', 'error')
  } finally {
    weatherBusy.value = false
  }
}

async function disableWeather() {
  if (weatherBusy.value) return
  const ok = window.confirm('关闭后不再推送每日天气，已保存的地点配置会被删除。确定关闭吗？')
  if (!ok) return
  weatherBusy.value = true
  try {
    await deleteWeatherConfig()
    weatherCfg.value = null
    weatherPreview.value = null
    notifyWeather('已关闭天气提醒')
  } catch (err: any) {
    notifyWeather(err.response?.data?.message || '关闭失败', 'error')
  } finally {
    weatherBusy.value = false
  }
}

async function loadMeta() {
  loading.value = true
  errorMsg.value = ''
  try {
    const res = await getUserConfigMeta()
    if (res.data?.code === 0) {
      items.value = Array.isArray(res.data.data) ? res.data.data : []
    } else {
      errorMsg.value = res.data?.message || '加载配置失败'
    }
  } catch (err: any) {
    errorMsg.value = err.response?.data?.message || '网络错误'
  } finally {
    loading.value = false
  }
}

async function handleSave(key: string, value: string) {
  errorMsg.value = ''
  try {
    const res = await updateConfig(key, value)
    if (res.data?.code === 0) {
      const idx = items.value.findIndex((it) => it.key === key)
      if (idx >= 0) {
        items.value[idx] = { ...items.value[idx], value }
      }
      listRef.value?.markSaved(key)
      if (key === 'theme_mode') {
        const themeStore = useThemeStore()
        if (themeStore.mode !== value) {
          themeStore.setMode(value as 'light' | 'dark' | 'system')
        }
      }
    } else {
      errorMsg.value = res.data?.message || '保存失败'
      listRef.value?.markFailed(key)
    }
  } catch (err: any) {
    errorMsg.value = err.response?.data?.message || '网络错误'
    listRef.value?.markFailed(key)
  }
}
</script>