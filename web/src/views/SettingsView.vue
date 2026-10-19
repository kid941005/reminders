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
import { onMounted, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import ConfigGroupList from '../components/ConfigGroupList.vue'
import { getUserConfigMeta, updateConfig } from '../api/config'
import { getCalendarFeed, resetCalendarFeed } from '../api/countdown'
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

onMounted(() => {
  loadMeta()
  loadFeed()
})

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
