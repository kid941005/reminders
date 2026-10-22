import request from './request'

export type ReminderChannel = 'inapp' | 'email' | 'sms' | 'feishu' | 'qq' | 'dingtalk' | 'bark'

export interface ReminderList {
  id: number
  name: string
  color: string
  icon: string
  position: number
  is_default: boolean
  open_count: number
}

export interface ReminderItem {
  id: number
  list_id: number
  list_name: string
  title: string
  notes: string
  priority: number
  due_at: string | null
  all_day: boolean
  repeat_rule: string
  calendar: string
  lunar_anchor?: string
  completed_at: string | null
  snoozed_until: string | null
  version: number
  channels: ReminderChannel[]
  channel_targets?: Record<string, number[]>
  repeat_notify_minutes: number
  /** 提前多少分钟提醒，0=准时 */
  remind_before_minutes: number
  /** 过期后最多重复几次，0=不限 */
  repeat_notify_times: number
  created_at: string
  updated_at: string
}

export interface SaveReminderInput {
  title: string
  notes?: string
  channel_targets?: Record<string, number[]>
  repeat_notify_minutes?: number
  remind_before_minutes?: number
  repeat_notify_times?: number
  list_id?: number
  priority?: number
  due_at?: string | null
  all_day?: boolean
  repeat_rule?: string
  calendar?: string
  lunar_anchor?: string
  channels?: ReminderChannel[]
  version?: number
}

export interface ChannelBindingItem {
  id: number
  target_masked: string
  status: string
}

export interface LunarDayInfo {
  year: number
  month: number
  day: number
  lunar: string
  lunar_month: number
  lunar_day: number
  lunar_year_name?: string
  term?: string
  festival?: string
  in_month: boolean
}

export interface HolidayDay {
  name: string
  /** true=放假，false=调休上班 */
  off: boolean
}

export interface HolidayRange {
  days: Record<string, HolidayDay>
  /** 内置数据实际覆盖的年份范围，用于判断「这一年公告尚未发布」 */
  min_year: number
  max_year: number
}

export interface LunarCalendarMonth {
  year: number
  month: number
  days: LunarDayInfo[]
}

export interface LunarConvertResult {
  lunar_year: number
  lunar_month: number
  lunar_day: number
  lunar_year_name: string
  year: number
  month: number
  day: number
}

export const getHolidays = (from: string, to: string) =>
  request.get('/api/reminder/holidays', { params: { from, to } })
export const getLunarCalendar = (year: number, month: number) =>
  request.get('/api/reminder/lunar', { params: { year, month } })

export const convertLunarDate = (params:
  | { year: number; month: number; day: number }
  | { lunar_year: number; lunar_month: number; lunar_day: number }
) => request.get<LunarConvertResult, { data: { data: LunarConvertResult } }>('/api/reminder/lunar/convert', { params })

export interface ChannelStatus {
  channel: ReminderChannel
  label: string
  configured: boolean
  bound: boolean
  status: string
  target_masked?: string
  bot_link?: string
  description: string
  bindings?: ChannelBindingItem[]
}

export const getSummary = () => request.get('/api/reminder/summary')
export const getLists = () => request.get('/api/reminder/lists')
export const createList = (data: { name: string; color?: string; icon?: string }) =>
  request.post('/api/reminder/lists', data)
export const updateList = (id: number, data: Partial<ReminderList>) =>
  request.patch(`/api/reminder/lists/${id}`, data)
export const deleteList = (id: number) => request.delete(`/api/reminder/lists/${id}`)

// from/to 仅对 view=calendar 生效，格式 YYYY-MM-DD，to 为开区间上界。
export const getReminders = (params: { view?: string; list_id?: number; q?: string; from?: string; to?: string }) =>
  request.get('/api/reminder/items', { params })
export const getReminder = (id: number) => request.get(`/api/reminder/items/${id}`)
export const createReminder = (data: SaveReminderInput) => request.post('/api/reminder/items', data)
export const updateReminder = (id: number, data: SaveReminderInput) =>
  request.put(`/api/reminder/items/${id}`, data)
export const deleteReminder = (id: number) => request.delete(`/api/reminder/items/${id}`)
export const completeReminder = (id: number) => request.post(`/api/reminder/items/${id}/complete`)
export const restoreReminder = (id: number) => request.post(`/api/reminder/items/${id}/restore`)
export const snoozeReminder = (id: number, until: string) =>
  request.post(`/api/reminder/items/${id}/snooze`, { until })

export const getNotifications = () => request.get('/api/reminder/notifications')
export const getUnreadCount = () => request.get('/api/reminder/notifications/unread-count')
export const markNotificationRead = (id: number) => request.post(`/api/reminder/notifications/${id}/read`)
export const markAllNotificationsRead = () => request.post('/api/reminder/notifications/read-all')
// 跨设备同步的轻量探针：只回一个单调递增的修订号。SSE 万一被中间代理缓冲，前端
// 靠定时比对它也能发现「另一台设备改过数据」并刷新（见 MainLayout 的 pollRevision）。
export const getRevision = () => request.get('/api/reminder/revision')

export const getChannelStatuses = () => request.get('/api/reminder/channels')
// secret 目前只被钉钉加签使用：留空表示沿用「自定义关键词」安全设置。
export const bindChannel = (channel: ReminderChannel, target: string, secret?: string) =>
  request.put(`/api/reminder/channels/${channel}`, secret ? { target, secret } : { target })
export const toggleChannel = (channel: ReminderChannel, enabled: boolean) =>
  request.patch(`/api/reminder/channels/${channel}`, { enabled })
export const unbindChannel = (channel: ReminderChannel) =>
  request.delete(`/api/reminder/channels/${channel}`)
export const unbindChannelTarget = (channel: ReminderChannel, id: number) =>
  request.delete(`/api/reminder/channels/${channel}/bindings/${id}`)
export const testChannel = (channel: ReminderChannel) =>
  request.post(`/api/reminder/channels/${channel}/test`)
export const getProviderStatuses = () => request.get('/api/reminder/admin/providers')
export const getNotificationBrand = () => request.get('/api/reminder/admin/notification-brand')
export const saveNotificationBrand = (name: string) => request.put('/api/reminder/admin/notification-brand', { name })
export const saveFeishuProvider = (data: { app_id: string; app_secret: string }) =>
  request.put('/api/reminder/admin/providers/feishu', data)
export const saveProvider = (provider: 'email' | 'sms' | 'qq', data: Record<string, string>) =>
  request.put(`/api/reminder/admin/providers/${provider}`, data)
export const createQQBindCode = () => request.post('/api/reminder/channels/qq/bind-code')

// 钉钉日程（CalDAV）单向导入。password 只在保存时提交，读取接口从不返回它。
export interface CalendarSyncConfig {
  id: number
  server: string
  calendar_url: string
  username: string
  list_id: number
  list_name: string
  enabled: boolean
  window_back_days: number
  window_forward_days: number
  remind_before_minutes: number
  last_sync_at: string | null
  last_status: string
  last_error: string
  last_imported: number
}
export interface CalendarSyncInput {
  server: string
  username: string
  password?: string
  enabled?: boolean
  window_back_days?: number
  window_forward_days?: number
  remind_before_minutes?: number
}
export const getCalendarSync = () => request.get('/api/reminder/calendar-sync')
export const saveCalendarSync = (data: CalendarSyncInput) => request.put('/api/reminder/calendar-sync', data)
export const deleteCalendarSync = () => request.delete('/api/reminder/calendar-sync')
export const runCalendarSync = () => request.post('/api/reminder/calendar-sync/run')
