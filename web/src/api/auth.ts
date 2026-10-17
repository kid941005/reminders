import request from './request'

export function login(username: string, password: string) {
  return request.post('/api/auth/login', {
    username,
    password,
  })
}

export function register(username: string, password: string) {
  return request.post('/api/auth/register', {
    username,
    password,
  })
}

export function checkAuth() {
  return request.get('/api/auth/check')
}

// 退出登录：幂等，401 属预期分支——会话本来就失效时也必须能退干净，
// 所以跳过全局的「401 即跳登录页」。
export function logout() {
  return request.post('/api/auth/logout', null, {
    headers: { 'X-Skip-Auth-Redirect': '1' },
  })
}

export function getCurrentUser() {
  return request.get('/api/auth/me')
}

export function checkSetupRequired() {
  return request.get('/api/auth/setup-required')
}

export function changePassword(oldPassword: string, newPassword: string) {
  return request.put('/api/auth/password', {
    old_password: oldPassword,
    new_password: newPassword,
  })
}

export function regenerateAPIKey() {
  return request.post('/api/auth/apikey')
}
