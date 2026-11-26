import request from './request'

export interface ConfigMeta {
  key: string
  scope: 'system' | 'user'
  type: 'string' | 'bool' | 'int' | 'select'
  value: string
  default: string
  public: boolean
  group: string
  label: string
  description: string
  options?: string[]
}

export function getPublicConfigs() {
  return request.get('/api/configs/public')
}

export function getSystemConfigs() {
  return request.get('/api/configs/system')
}

export function getAllConfigs() {
  return request.get('/api/configs')
}

export function getSystemConfigMeta() {
  return request.get('/api/configs/meta')
}

export function getUserConfigMeta() {
  return request.get('/api/configs/user/meta')
}

export function updateConfig(key: string, value: string) {
  return request.put('/api/configs', { key, value })
}

export function getVersion() {
  return request.get('/api/version')
}

export interface AppNavItem {
  name: string
  display_name: string
  icon?: string
  route_prefix: string
  nav_position: number
}

export function getApps() {
  return request.get('/api/apps')
}
