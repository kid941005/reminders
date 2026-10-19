import request from './request'

export interface CountdownItem {
  id: number
  title: string
  target_date: string
  repeat_yearly: boolean
  calendar: 'solar' | 'lunar'
  lunar_anchor?: string
  notes: string
  color: string
  position: number
  /** 下一次发生的日期（后端已按农历/闰年算好） */
  next_date: string
  /** 相对今天：正数=还有几天，0=今天，负数=已过去几天 */
  days_left: number
}

export interface SaveCountdownInput {
  title: string
  target_date: string
  repeat_yearly: boolean
  calendar: string
  notes: string
  color: string
}

export const getCountdowns = () => request.get('/api/reminder/countdowns')
export const createCountdown = (data: SaveCountdownInput) =>
  request.post('/api/reminder/countdowns', data)
export const updateCountdown = (id: number, data: SaveCountdownInput) =>
  request.put(`/api/reminder/countdowns/${id}`, data)
export const deleteCountdown = (id: number) => request.delete(`/api/reminder/countdowns/${id}`)

// ICS 订阅：令牌失效后旧地址立即不可用，所以提供重置。
export const getCalendarFeed = () => request.get('/api/reminder/feed')
export const resetCalendarFeed = () => request.post('/api/reminder/feed/reset')
