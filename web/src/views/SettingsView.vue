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
import { onMounted, reactive, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import ConfigGroupList from '../components/ConfigGroupList.vue'
import { getUserConfigMeta, updateConfig } from '../api/config'
import { getCalendarFeed, resetCalendarFeed } from '../api/countdown'
import { deleteCalendarSync, getCalendarSync, runCalendarSync, saveCalendarSync } from '../api/reminder'
import type { CalendarSyncConfig } from '../api/reminder'
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

onMounted(() => {
  loadMeta()
  loadFeed()
  loadCalendarSync()
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
