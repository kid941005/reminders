import {
  defineStore
} from 'pinia'
import {
  ref,
  computed
} from 'vue'
import { SK } from '../utils/storage-keys'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ThemeBrand = 'default'  // extend this union when adding brand-* palettes

// 与 index.html 首屏脚本、utils/storage-keys.ts 共用同一组键。
const STORAGE_MODE = SK.themeMode
const STORAGE_BRAND = SK.themeBrand

// 旧版本用的是不带前缀的裸键。首次读取时迁移到新键，避免升级后主题被重置。
const LEGACY_MODE = 'reminder-theme-mode'
const LEGACY_BRAND = 'reminder-theme-brand'

function readWithLegacy(key: string, legacyKey: string): string | null {
  const current = localStorage.getItem(key)
  if (current !== null) return current
  const legacy = localStorage.getItem(legacyKey)
  if (legacy !== null) localStorage.setItem(key, legacy)
  return legacy
}

function readStoredMode(): ThemeMode {
  const v = readWithLegacy(STORAGE_MODE, LEGACY_MODE)
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'system'
}

function readStoredBrand(): ThemeBrand {
  const v = readWithLegacy(STORAGE_BRAND, LEGACY_BRAND)
  return (v as ThemeBrand) || 'default'
}

const systemDark = ref(
  typeof window !== 'undefined'
    && window.matchMedia('(prefers-color-scheme: dark)').matches,
)

if (typeof window !== 'undefined') {
  window.matchMedia('(prefers-color-scheme: dark)')
    .addEventListener('change', e => { systemDark.value = e.matches })
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const brand = ref<ThemeBrand>(readStoredBrand())

  // isDark tracks mode AND the OS preference (via systemDark ref) so
  // consumers bound to it update when the OS theme flips in 'system'.
  const isDark = computed(() =>
    mode.value === 'dark' || (mode.value === 'system' && systemDark.value),
  )

  function resolveDark(): boolean {
    if (mode.value === 'dark') return true
    if (mode.value === 'light') return false
    return systemDark.value
  }

  function apply() {
    const root = document.documentElement
    root.classList.toggle('dark', resolveDark())

    for (const cls of Array.from(root.classList)) {
      if (cls.startsWith('brand-') && cls !== `brand-${brand.value}`) {
        root.classList.remove(cls)
      }
    }
    if (brand.value !== 'default') {
      root.classList.add(`brand-${brand.value}`)
    }
  }

  // 本地状态 + localStorage 是唯一写入口；需要同步到服务端配置的地方
  // （顶部切换按钮）由调用方负责发请求，避免 store 反向依赖 api 层。
  function setMode(m: ThemeMode) {
    mode.value = m
    localStorage.setItem(STORAGE_MODE, m)
    apply()
  }

  // 由服务端配置驱动时使用：语义上区分「用户主动选择」与「跟随服务端」。
  function setModeFromServer(m: ThemeMode) {
    setMode(m)
  }

  function setBrand(b: ThemeBrand) {
    brand.value = b
    localStorage.setItem(STORAGE_BRAND, b)
    apply()
  }

  // 3-state cycle documented in AGENTS.md. Components should call this so
  // the cycle stays in one place.
  function toggle() {
    const next: ThemeMode = mode.value === 'dark'
      ? 'light'
      : mode.value === 'light'
        ? 'system'
        : 'dark'
    setMode(next)
  }

  apply()

  return { mode, brand, isDark, toggle, setMode, setModeFromServer, setBrand, apply }
})
