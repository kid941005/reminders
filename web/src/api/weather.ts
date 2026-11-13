import request from './request'
import type { ReminderChannel } from './reminder'

/** 和风天气的每用户配置；API Key 属于管理员，前端永远不会拿到它。 */
export interface WeatherConfig {
  configured: boolean
  admin_configured: boolean
  id: number
  enabled: boolean
  location_name: string
  latitude: number
  longitude: number
  morning_time: string
  channels: ReminderChannel[]
  last_push_date: string
  last_push_at: string | null
  last_status: string
  last_error: string
}

export interface WeatherConfigInput {
  location_name: string
  latitude: number
  longitude: number
  morning_time: string
  channels: ReminderChannel[]
  enabled?: boolean
}

export interface WeatherLocation {
  name: string
  id: string
  lat: string
  lon: string
  adm1: string
  adm2: string
  tz: string
}

export interface WeatherMessage {
  title: string
  body: string
}

export interface WeatherAdminStatus {
  configured: boolean
  host: string
}

export const getWeatherConfig = () => request.get('/api/reminder/weather')
export const saveWeatherConfig = (data: WeatherConfigInput) =>
  request.put('/api/reminder/weather', data)
export const deleteWeatherConfig = () => request.delete('/api/reminder/weather')
export const runWeather = () => request.post('/api/reminder/weather/run')
export const previewWeather = () => request.get('/api/reminder/weather/preview')
export const lookupWeatherLocations = (q: string) =>
  request.get('/api/reminder/weather/locations', { params: { q } })

export const getWeatherAdmin = () => request.get('/api/reminder/admin/weather')
export const saveWeatherAdmin = (data: { host: string; api_key?: string }) =>
  request.put('/api/reminder/admin/weather', data)
