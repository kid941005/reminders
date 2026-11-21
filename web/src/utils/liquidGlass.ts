/**
 * 液态玻璃真实折射（SDF 透镜）
 *
 * 高分实现（shuding/liquid-glass、rdev/liquid-glass-react）的核心不是把
 * 整块背景做噪声扭曲，而是给每个面板生成一张「边缘位移、中心清晰」的
 * SDF（signed distance field）贴图，再交给 SVG feDisplacementMap 去弯折
 * backdrop。这样玻璃中心仍然通透，只有靠近圆角/边缘的一圈发生光学折射，
 * 配合红/绿/蓝三通道使用不同位移量，边缘会出现极细的色散（chromatic
 * aberration）——也就是液态玻璃最容易被认出来的特征。
 *
 * 这个模块在运行时按元素尺寸生成贴图并注册 <filter>，元素只通过
 * CSS 变量 --lg-filter 引用，不把任何尺寸写死在样式里。桌面端启用；
 * 触摸设备使用普通模糊回退，是否关闭液态玻璃由应用内开关决定。
 */

import { SK } from './storage-keys'

const SVG_NS = 'http://www.w3.org/2000/svg'
const TARGET_SELECTOR = '.surface, .glass-bar'
const MAP_MAX = 512
const MAP_MIN = 64
const FILTER_MARGIN = 48
const REDUCED_TRANSPARENCY_CLASS = 'lg-reduced-transparency'

type CacheEntry = { id: string }

const filterCache = new Map<string, CacheEntry>()
const observed = new WeakSet<Element>()
let defs: SVGSVGElement | null = null
let counter = 0
let scheduled = false

/** 读取用户是否启用液态玻璃；默认开启，旧版本没有该键时也保持开启。 */
export function getGlassEffectPreference(): boolean {
  if (typeof localStorage === 'undefined') return true
  try {
    return localStorage.getItem(SK.glassEffect) !== 'false'
  } catch {
    return true
  }
}

/** 只切换 CSS 回退类，不重建滤镜；关闭时仍保留已生成的贴图，便于即时恢复。 */
export function applyGlassEffectPreference(enabled: boolean): void {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle(REDUCED_TRANSPARENCY_CLASS, !enabled)
}

/** 持久化用户选择并立即应用。 */
export function setGlassEffectPreference(enabled: boolean): void {
  if (typeof localStorage !== 'undefined') {
    try {
      localStorage.setItem(SK.glassEffect, enabled ? 'true' : 'false')
    } catch {}
  }
  applyGlassEffectPreference(enabled)
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function smoothstep(edge0: number, edge1: number, x: number): number {
  if (edge0 === edge1) return x < edge0 ? 0 : 1
  const t = clamp((x - edge0) / (edge1 - edge0), 0, 1)
  return t * t * (3 - 2 * t)
}

/** 圆角矩形的有符号距离：内部为负，边界为 0，外部为正。 */
function roundedRectSDF(x: number, y: number, halfW: number, halfH: number, radius: number): number {
  const qx = Math.abs(x) - (halfW - radius)
  const qy = Math.abs(y) - (halfH - radius)
  return Math.min(Math.max(qx, qy), 0) + Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) - radius
}

/**
 * 生成位移贴图。R/G 通道分别编码 x/y 方向的采样偏移（0.5 = 不偏移），
 * 只有靠近面板边缘的一圈有值；中心与大部分区域保持 0.5，所以中心清晰。
 */
function buildDisplacementMap(width: number, height: number, radius: number): string {
  const ratio = Math.min(1, MAP_MAX / Math.max(width, height))
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(MAP_MIN, Math.round(width * ratio))
  canvas.height = Math.max(MAP_MIN, Math.round(height * ratio))
  const ctx = canvas.getContext('2d', { willReadFrequently: true })
  if (!ctx) return ''

  const image = ctx.createImageData(canvas.width, canvas.height)
  const data = image.data
  const halfW = width / 2
  const halfH = height / 2
  const r = clamp(radius, 0, Math.min(halfW, halfH))
  const bezel = clamp(Math.min(width, height) * 0.1, 10, 30)

  for (let row = 0; row < canvas.height; row += 1) {
    const py = ((row + 0.5) / canvas.height) * height
    for (let col = 0; col < canvas.width; col += 1) {
      const px = ((col + 0.5) / canvas.width) * width
      const dxCenter = px - halfW
      const dyCenter = py - halfH
      const distance = roundedRectSDF(dxCenter, dyCenter, halfW, halfH, r)

      // 边缘 1 → 内侧 0；面板外（圆角切掉的部分）直接为 0。
      const edge = distance >= 0 ? 0 : 1 - smoothstep(-bezel, 0, distance)

      // 用 SDF 梯度求“向内的法线”，圆角处也能得到正确的斜向位移。
      const eps = 1
      const gradX =
        roundedRectSDF(dxCenter + eps, dyCenter, halfW, halfH, r) -
        roundedRectSDF(dxCenter - eps, dyCenter, halfW, halfH, r)
      const gradY =
        roundedRectSDF(dxCenter, dyCenter + eps, halfW, halfH, r) -
        roundedRectSDF(dxCenter, dyCenter - eps, halfW, halfH, r)
      const gradientLength = Math.hypot(gradX, gradY) || 1
      const inwardX = -gradX / gradientLength
      const inwardY = -gradY / gradientLength

      // 0.5 是 feDisplacementMap 的零点；乘 0.5 让最强处取到 0/1。
      const offset = edge * 0.5
      const index = (row * canvas.width + col) * 4
      data[index] = Math.round(clamp(0.5 + inwardX * offset, 0, 1) * 255)
      data[index + 1] = Math.round(clamp(0.5 + inwardY * offset, 0, 1) * 255)
      data[index + 2] = 128
      data[index + 3] = 255
    }
  }

  ctx.putImageData(image, 0, 0)
  return canvas.toDataURL('image/png')
}

function createColorMatrix(matrix: string, result: string): SVGFEColorMatrixElement {
  const node = document.createElementNS(SVG_NS, 'feColorMatrix')
  node.setAttribute('in', 'SourceGraphic')
  node.setAttribute('type', 'matrix')
  node.setAttribute('values', matrix)
  node.setAttribute('result', result)
  return node
}

function createFilter(width: number, height: number, radius: number): string {
  if (!defs) return ''
  const id = `lg-lens-${++counter}`
  const filter = document.createElementNS(SVG_NS, 'filter')
  filter.setAttribute('id', id)
  filter.setAttribute('filterUnits', 'userSpaceOnUse')
  filter.setAttribute('primitiveUnits', 'userSpaceOnUse')
  filter.setAttribute('x', String(-FILTER_MARGIN))
  filter.setAttribute('y', String(-FILTER_MARGIN))
  filter.setAttribute('width', String(width + FILTER_MARGIN * 2))
  filter.setAttribute('height', String(height + FILTER_MARGIN * 2))
  filter.setAttribute('color-interpolation-filters', 'sRGB')

  const mapHref = buildDisplacementMap(width, height, radius)
  const mapImage = document.createElementNS(SVG_NS, 'feImage')
  mapImage.setAttribute('href', mapHref)
  mapImage.setAttribute('x', '0')
  mapImage.setAttribute('y', '0')
  mapImage.setAttribute('width', String(width))
  mapImage.setAttribute('height', String(height))
  mapImage.setAttribute('preserveAspectRatio', 'none')
  mapImage.setAttribute('result', 'lg-map')
  filter.appendChild(mapImage)

  // 色散强度随尺寸缩放：大面板边缘宽，微小的通道错位才看得见；
  // 小面板保持克制，避免一眼就是彩虹边。
  const scale = clamp(Math.min(width, height) * 0.075, 14, 32)
  const aberration = clamp(scale * 0.08, 1.2, 3.0)

  // 三通道各自位移后 screen 合成：中心三通道重合 → 原色；边缘因位移量
  // 不同而分离 → 红/蓝各偏一点，形成真实的玻璃切边色散。
  filter.appendChild(createColorMatrix('1 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 1 0', 'lg-red'))
  filter.appendChild(createColorMatrix('0 0 0 0 0  0 1 0 0 0  0 0 0 0 0  0 0 0 1 0', 'lg-green'))
  filter.appendChild(createColorMatrix('0 0 0 0 0  0 0 0 0 0  0 0 1 0 0  0 0 0 1 0', 'lg-blue'))

  const channels: Array<[string, number, string]> = [
    ['lg-red', scale - aberration, 'lg-red-shift'],
    ['lg-green', scale, 'lg-green-shift'],
    ['lg-blue', scale + aberration, 'lg-blue-shift'],
  ]
  channels.forEach(([source, channelScale, result]) => {
    const displacement = document.createElementNS(SVG_NS, 'feDisplacementMap')
    displacement.setAttribute('in', source)
    displacement.setAttribute('in2', 'lg-map')
    displacement.setAttribute('scale', String(channelScale))
    displacement.setAttribute('xChannelSelector', 'R')
    displacement.setAttribute('yChannelSelector', 'G')
    displacement.setAttribute('result', result)
    filter.appendChild(displacement)
  })

  const blendRG = document.createElementNS(SVG_NS, 'feBlend')
  blendRG.setAttribute('in', 'lg-red-shift')
  blendRG.setAttribute('in2', 'lg-green-shift')
  blendRG.setAttribute('mode', 'screen')
  blendRG.setAttribute('result', 'lg-rg')
  filter.appendChild(blendRG)

  const blendRGB = document.createElementNS(SVG_NS, 'feBlend')
  blendRGB.setAttribute('in', 'lg-rg')
  blendRGB.setAttribute('in2', 'lg-blue-shift')
  blendRGB.setAttribute('mode', 'screen')
  filter.appendChild(blendRGB)

  defs.appendChild(filter)
  return id
}

function readRadius(element: Element): number {
  const style = getComputedStyle(element)
  const radius = parseFloat(style.borderTopLeftRadius || '0')
  return Number.isFinite(radius) ? radius : 0
}

function applyLens(element: HTMLElement): void {
  if (!element.isConnected) return
  const rect = element.getBoundingClientRect()
  if (rect.width < 32 || rect.height < 32) return

  const width = Math.round(rect.width)
  const height = Math.round(rect.height)
  const radius = readRadius(element)
  const key = `${width}x${height}x${Math.round(radius)}`

  let entry = filterCache.get(key)
  if (!entry) {
    const id = createFilter(width, height, radius)
    if (!id) return
    entry = { id }
    filterCache.set(key, entry)
  }

  if (element.dataset.lgFilter !== entry.id) {
    element.style.setProperty('--lg-filter', `url(#${entry.id})`)
    element.dataset.lgFilter = entry.id
  }
  element.classList.add('lg-lens-ready')
}

function scan(): void {
  if (!defs) return
  document.querySelectorAll<HTMLElement>(TARGET_SELECTOR).forEach(applyLens)
}

function scheduleScan(): void {
  if (scheduled) return
  scheduled = true
  requestAnimationFrame(() => {
    scheduled = false
    scan()
  })
}

const resizeObserver = new ResizeObserver((entries) => {
  entries.forEach((entry) => {
    if (entry.target instanceof HTMLElement) applyLens(entry.target)
  })
})

function observe(element: Element): void {
  if (observed.has(element)) return
  observed.add(element)
  resizeObserver.observe(element)
}

export function initLiquidGlass(): void {
  if (typeof window === 'undefined' || typeof document === 'undefined') return
  // 系统“降低透明度”只作为默认提示，不强制降级：用户可在设置里关闭液态玻璃。
  applyGlassEffectPreference(getGlassEffectPreference())
  if (window.matchMedia('(hover: none), (pointer: coarse)').matches) return
  const supportsUrlFilter =
    CSS.supports('backdrop-filter', 'url(#lg-probe)') ||
    CSS.supports('-webkit-backdrop-filter', 'url(#lg-probe)')
  if (!supportsUrlFilter) return

  defs = document.createElementNS(SVG_NS, 'svg')
  defs.setAttribute('id', 'lg-filter-defs')
  defs.setAttribute('width', '0')
  defs.setAttribute('height', '0')
  defs.setAttribute('aria-hidden', 'true')
  defs.setAttribute('focusable', 'false')
  defs.style.position = 'absolute'
  defs.style.pointerEvents = 'none'
  document.body.appendChild(defs)
  document.documentElement.classList.add('lg-glass')

  const start = () => {
    document.querySelectorAll<HTMLElement>(TARGET_SELECTOR).forEach((element) => {
      observe(element)
      applyLens(element)
    })
    const observer = new MutationObserver((mutations) => {
      let hasNewTarget = false
      mutations.forEach((mutation) => {
        mutation.addedNodes.forEach((node) => {
          if (!(node instanceof Element)) return
          if (node.matches(TARGET_SELECTOR)) {
            observe(node)
            hasNewTarget = true
          }
          node.querySelectorAll(TARGET_SELECTOR).forEach((el) => {
            observe(el)
            hasNewTarget = true
          })
        })
      })
      // 只有真正新增了玻璃目标才重扫；Vue 的文本/属性更新不必触发全量 getBoundingClientRect。
      if (hasNewTarget) scheduleScan()
    })
    observer.observe(document.body, { childList: true, subtree: true })
    window.addEventListener('resize', scheduleScan, { passive: true })
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start, { once: true })
  } else {
    start()
  }
}
