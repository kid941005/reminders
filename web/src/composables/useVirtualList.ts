import { computed, ref, type Ref } from 'vue'

export interface UseVirtualListOptions {
  /** 每行固定高度（px），虚拟滚动按此常量定位与计算可见窗口。 */
  itemHeight: number
  /** 虚拟视口高度（px），超过即出现内部滚动条；默认 600。 */
  viewportHeight?: number
  /** 视口上下额外渲染的缓冲行数；默认 4。 */
  overscan?: number
  /** 数据量「大于」该阈值才启用虚拟化，否则渲染完整 DOM；默认 30。 */
  threshold?: number
}

export interface VirtualRow<T> {
  /** 数据在源数组中的绝对下标，用于换算绝对定位偏移。 */
  index: number
  item: T
}

/**
 * 通用固定行高虚拟滚动。
 * - 仅当 source 长度大于 threshold 时才虚拟化（只渲染可见窗口 + overscan）；
 * - 少量数据时 renderedItems 即全部数据，DOM 结构与原来一致。
 */
export function useVirtualList<T>(source: Ref<T[]>, options: UseVirtualListOptions) {
  const itemHeight = Math.max(1, options.itemHeight)
  const viewportHeight = options.viewportHeight ?? 600
  const overscan = Math.max(0, options.overscan ?? 4)
  const threshold = Math.max(0, options.threshold ?? 30)

  const scrollEl = ref<HTMLElement | null>(null)
  const scrollTop = ref(0)

  const isVirtual = computed(() => source.value.length > threshold)
  const totalHeight = computed(() => source.value.length * itemHeight)

  const startIndex = computed(() => {
    if (!isVirtual.value) return 0
    const raw = Math.floor(scrollTop.value / itemHeight) - overscan
    const maxStart = Math.max(0, source.value.length - 1)
    return Math.max(0, Math.min(raw, maxStart))
  })

  const endIndex = computed(() => {
    if (!isVirtual.value) return source.value.length
    const visibleCount = Math.ceil(viewportHeight / itemHeight) + overscan * 2
    return Math.min(source.value.length, startIndex.value + visibleCount)
  })

  const renderedItems = computed<VirtualRow<T>[]>(() => {
    const rows: VirtualRow<T>[] = []
    for (let i = startIndex.value; i < endIndex.value; i += 1) {
      const item = source.value[i]
      if (item !== undefined) rows.push({ index: i, item })
    }
    return rows
  })

  function handleScroll() {
    if (scrollEl.value) scrollTop.value = scrollEl.value.scrollTop
  }

  function scrollToTop() {
    if (scrollEl.value) scrollEl.value.scrollTop = 0
    scrollTop.value = 0
  }

  return {
    scrollEl,
    scrollTop,
    isVirtual,
    totalHeight,
    startIndex,
    endIndex,
    renderedItems,
    viewportHeight,
    itemHeight,
    handleScroll,
    scrollToTop,
  }
}
