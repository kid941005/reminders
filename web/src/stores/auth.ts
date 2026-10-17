import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { checkAuth, checkSetupRequired as apiCheckSetupRequired, logout as apiLogout } from '../api/auth'
import { getBootstrap, type BootstrapData } from '../api/bootstrap'
import { getUserConfigMeta } from '../api/config'
import { getSecurityQuestions } from '../api/security'
import { useThemeStore } from './theme'
import { withTimeout } from '../utils/async'
import { setBootHint } from '../utils/boot'
import { SK } from '../utils/storage-keys'

// 启动阶段单次请求的上限：超过就按默认值继续渲染，绝不无限等。取 4 秒是为
// 了容忍低端设备首帧较慢，又不至于让弱网下一直停在首屏骨架上——启动骨架
// 已经替掉了白屏，用户对「稍等」的容忍度远高于对「一片空白」的。
const STARTUP_TIMEOUT_MS = 4000
// 服务可能还在初始化（监听已就绪、建库迁移未完成时返回 503 + Retry-After），
// 这时重试比直接按默认值渲染更正确：默认值会把首次安装的用户带到登录页。
const STARTUP_MAX_ATTEMPTS = 3
const STARTUP_RETRY_DELAY_MS = 1200

function delay(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem(SK.token) || '')
  const user = ref<any>(null)
  const requireLogin = ref(true)
  const allowRegister = ref(true)
  const setupRequired = ref(false)
  const siteTitle = ref('提醒事项')
  watch(siteTitle, (t) => { document.title = t }, { immediate: true })
  const hasSecurityQuestions = ref(true)
  const DISMISS_KEY = SK.securityPromptDismissed
  const DISMISS_TTL = 60 * 60 * 1000 // 1 hour
  const securityPromptDismissed = ref((() => {
    const ts = localStorage.getItem(DISMISS_KEY)
    if (!ts) return false
    return Date.now() - Number(ts) < DISMISS_TTL
  })())
  let initialized = false
  let initPromise: Promise<void> | null = null

  // 登录态只看有没有用户：token 只决定「要不要往请求里带」。
  const isAuthenticated = computed(() => !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')

  function clearSession() {
    token.value = ''
    user.value = null
    localStorage.removeItem(SK.token)
  }

  function applyBootstrap(data: BootstrapData) {
    setupRequired.value = !!data.setup_required
    const configs = data.configs || {}
    requireLogin.value = configs.require_login !== 'false'
    allowRegister.value = configs.allow_register !== 'false'
    if (configs.site_title) siteTitle.value = configs.site_title
  }

  async function init() {
    if (initialized) return
    if (initPromise) return initPromise

    initPromise = (async () => {
      // 启动只发一次引导请求：公开配置、初始化状态、登录态校验都在里面。
      // 请求并行、串行都改不了弱网的往返次数，合并接口才是真正省掉的那两跳。
      let data: BootstrapData | null = null
      for (let attempt = 0; attempt < STARTUP_MAX_ATTEMPTS && !data; attempt++) {
        if (attempt > 0) {
          setBootHint('服务正在启动，请稍候…')
          await delay(STARTUP_RETRY_DELAY_MS)
        }
        const res = await withTimeout(getBootstrap(), STARTUP_TIMEOUT_MS, null)
        if (!res) continue
        if (res.data?.code === 0) data = res.data.data as BootstrapData
        // 服务已应答（含错误应答）：重试不会改变结果。
        break
      }

      if (data) {
        applyBootstrap(data)
        if (setupRequired.value && token.value) {
          clearSession()
          return
        }
        if (!token.value) return
        const auth = data.auth
        if (auth?.authenticated && auth.user) {
          user.value = auth.user
          // 安全提示与主题偏好都不参与路由决策，放到后台补齐，绝不阻塞首屏。
          void loadUserPreferences()
        } else {
          clearSession()
        }
        return
      }

      // 引导请求全部失败（服务未就绪）：按默认值继续渲染，至少页面能出来。
      // 已持有令牌时仍单独确认一次，避免网络抖动把已登录用户请到登录页。
      setBootHint('网络较慢，正在加载应用…')
      if (token.value) {
        const authRes = await withTimeout(checkAuth(), STARTUP_TIMEOUT_MS, null)
        if (authRes?.data?.code === 0 && authRes.data.data?.authenticated) {
          user.value = authRes.data.data.user
          void loadUserPreferences()
        } else {
          clearSession()
        }
      }
    })().finally(() => {
      initialized = true
    })

    return initPromise
  }

  // 登录后的补充信息：安全提示状态与用户主题偏好。失败静默，不阻塞首屏。
  async function loadUserPreferences() {
    try {
      const secRes = await getSecurityQuestions()
      if (secRes.data?.code === 0) {
        hasSecurityQuestions.value = !!secRes.data.data?.has_questions
      }
    } catch {}
    try {
      const metaRes = await getUserConfigMeta()
      if (metaRes.data?.code === 0 && Array.isArray(metaRes.data.data)) {
        const themeItem = metaRes.data.data.find((it: any) => it.key === 'theme_mode')
        if (themeItem && ['system', 'light', 'dark'].includes(themeItem.value)) {
          const themeStore = useThemeStore()
          if (themeStore.mode !== themeItem.value) {
            themeStore.setMode(themeItem.value)
          }
        }
      }
    } catch {}
  }

  function resetInit() {
    initialized = false
    initPromise = null
  }

  function setToken(newToken: string) {
    token.value = newToken
    localStorage.setItem(SK.token, newToken)
  }

  function setUser(newUser: any) {
    user.value = newUser
  }

  // 只清本地登录态。401 拦截器用它做强制清理，endSession 也以它收尾。
  function clearLocalSession() {
    token.value = ''
    user.value = null
    localStorage.removeItem(SK.token)
    localStorage.removeItem(DISMISS_KEY)
    securityPromptDismissed.value = false
  }

  // 退出登录（用户点击）：先通知服务端（审计日志），再清本地。请求失败也继续
  // 本地登出——宁可本地先退出，也不能把用户卡在已登录界面里。
  async function endSession() {
    try {
      await apiLogout()
    } catch {}
    clearLocalSession()
  }

  // 兼容旧调用点（401 拦截器等）：同步清本地，不等服务端。
  function logout() {
    clearLocalSession()
  }

  async function checkSetupRequired() {
    try {
      const setupRes = await apiCheckSetupRequired()
      if (setupRes.data?.code === 0) {
        setupRequired.value = !!setupRes.data.data?.setup_required
      }
    } catch {}
    return setupRequired.value
  }

  function dismissSecurityPrompt() {
    securityPromptDismissed.value = true
    localStorage.setItem(DISMISS_KEY, String(Date.now()))
  }

  function refreshSecurityQuestions() {
    if (!token.value) return
    getSecurityQuestions()
      .then(res => {
        if (res.data?.code === 0) {
          hasSecurityQuestions.value = !!res.data.data?.has_questions
          if (hasSecurityQuestions.value) {
            localStorage.removeItem(DISMISS_KEY)
          }
        }
      })
      .catch(() => {})
  }

  return { token, user, requireLogin, allowRegister, setupRequired, siteTitle, hasSecurityQuestions, securityPromptDismissed, isAuthenticated, isAdmin, init, resetInit, setToken, setUser, endSession, logout, checkSetupRequired, dismissSecurityPrompt, refreshSecurityQuestions }
})
