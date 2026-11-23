import { clientID } from '../utils/client-id'

export interface RealtimeNotification {
  id: number
  reminder_id?: number
  type: string
  title: string
  body: string
  read_at?: string
  created_at: string
}

export interface ReminderRealtimeEvent {
  /** notification.created（站内通知）/ reminders.changed（数据变更）/ connected */
  type: 'connected' | 'notification.created' | 'reminders.changed' | string
  notification?: RealtimeNotification
  /** reminders.changed：哪类数据变了（reminder / list / notification / channel）。 */
  scope?: string
  /** reminders.changed：发生了什么（created / updated / deleted / completed…）。 */
  action?: string
  target_id?: number
  /** 该用户的变更修订号，单调递增。 */
  revision?: number
  sent_at: string
}

/** 退避封顶（抖动后也封顶到此值）。 */
const MAX_BACKOFF_MS = 15_000
/** 初始退避基数。 */
const BASE_BACKOFF_MS = 1_000
/** 抖动系数区间：base * [0.8, 1.2]。 */
const JITTER_MIN = 0.8
const JITTER_SPAN = 0.4

const isPageHidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden'
const isOffline = () => typeof navigator !== 'undefined' && navigator.onLine === false

/** base * 随机 0.8–1.2 的抖动，并封顶 15s。 */
function withJitter(base: number): number {
  const jittered = base * (JITTER_MIN + Math.random() * JITTER_SPAN)
  return Math.min(MAX_BACKOFF_MS, Math.round(jittered))
}

export function connectReminderEvents(
  token: string,
  onEvent: (event: ReminderRealtimeEvent) => void,
): () => void {
  let stopped = false
  let controller: AbortController | undefined

  // 可被外部唤醒的等待：既用于「退避等待」，也用于「隐藏/离线时挂起」。
  let waitTimer: ReturnType<typeof setTimeout> | undefined
  let wakeCurrent: (() => void) | undefined

  function cancelWait() {
    if (waitTimer !== undefined) {
      clearTimeout(waitTimer)
      waitTimer = undefined
    }
    wakeCurrent?.()
  }

  /** 等待 ms 毫秒后自动唤醒；省略 ms 表示一直挂起，直到 cancelWait() 被调用。 */
  function waitFor(ms?: number) {
    return new Promise<void>(resolve => {
      let settled = false
      const finish = () => {
        if (settled) return
        settled = true
        if (waitTimer !== undefined) {
          clearTimeout(waitTimer)
          waitTimer = undefined
        }
        if (wakeCurrent === finish) wakeCurrent = undefined
        resolve()
      }
      wakeCurrent = finish
      if (ms !== undefined) waitTimer = setTimeout(finish, ms)
    })
  }

  async function run() {
    let retryBase = BASE_BACKOFF_MS
    while (!stopped) {
      // 页面隐藏或离线：完全不建立/保持连接，挂起等待唤醒信号。
      if (isPageHidden() || isOffline()) {
        await waitFor()
        continue
      }

      controller = new AbortController()
      let abortedByLifecycle = false
      try {
        // SSE 长连接需要自带 JWT：EventSource 无法设头，这里用 fetch 流式读取。
        const headers: Record<string, string> = { Accept: 'text/event-stream' }
        if (token) headers.Authorization = `Bearer ${token}`
        // client_id 让服务端广播数据变更时跳过本标签页（它已经在本地改过了）。
        // 必须相对应用基址解析：以 document.baseURI（当前页面 URL）为基准时，
        // /admin/day/2026-11-05 这类深层路由会解析成 /admin/day/api/...，
        // 请求落到 SPA 兜底页上，长连接静默失效。
        const url = new URL(`${import.meta.env.BASE_URL}api/reminder/events`, window.location.origin)
        const id = clientID()
        if (id) url.searchParams.set('client_id', id)
        const response = await fetch(url, {
          headers,
          cache: 'no-store',
          signal: controller.signal,
        })
        if (!response.ok || !response.body) {
          throw new Error(`SSE connection failed: ${response.status}`)
        }

        retryBase = BASE_BACKOFF_MS
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        while (!stopped) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')

          let boundary = buffer.indexOf('\n\n')
          while (boundary >= 0) {
            const block = buffer.slice(0, boundary)
            buffer = buffer.slice(boundary + 2)
            const data = block
              .split('\n')
              .filter(line => line.startsWith('data:'))
              .map(line => line.slice(5).trimStart())
              .join('\n')
            if (data) {
              try {
                onEvent(JSON.parse(data) as ReminderRealtimeEvent)
              } catch {
                // Ignore malformed events and keep the long connection alive.
              }
            }
            boundary = buffer.indexOf('\n\n')
          }
        }
      } catch (error) {
        if (stopped) return
        if (error instanceof DOMException && error.name === 'AbortError') {
          // 由「隐藏/离线」主动中断：不是失败，交给顶部循环判断挂起或立即重连。
          abortedByLifecycle = true
        }
      }

      if (stopped) return

      // 隐藏/离线：不重试，直接回到顶部挂起，恢复可见/在线时再立即重连（不等退避）。
      if (isPageHidden() || isOffline()) continue

      // 主动中断（例如刚由隐藏转可见的竞态）：立即重连，不进入退避。
      if (abortedByLifecycle) continue

      // 常规断线：带抖动退避后重连。
      await waitFor(withJitter(retryBase))
      retryBase = Math.min(retryBase * 2, MAX_BACKOFF_MS)
    }
  }

  // 可见性 / 网络状态变化：
  // - 隐藏或离线：立即掐断连接并取消所有等待；
  // - 恢复可见/在线：立即唤醒等待，不等退避就重连。
  function reconcile() {
    if (stopped) return
    if (isPageHidden() || isOffline()) {
      cancelWait()
      controller?.abort()
      return
    }
    cancelWait()
  }

  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', reconcile)
  if (typeof window !== 'undefined') {
    window.addEventListener('online', reconcile)
    window.addEventListener('offline', reconcile)
  }

  void run()

  return () => {
    stopped = true
    cancelWait()
    controller?.abort()
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', reconcile)
    if (typeof window !== 'undefined') {
      window.removeEventListener('online', reconcile)
      window.removeEventListener('offline', reconcile)
    }
  }
}
