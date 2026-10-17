import axios from 'axios'
import { useAuthStore } from '../stores/auth'
import router from '../router'
import { clientID } from '../utils/client-id'

const request = axios.create({
  baseURL: import.meta.env.BASE_URL,
  timeout: 30000,
})

request.interceptors.request.use((config) => {
	if (import.meta.env.BASE_URL !== '/' && config.url?.startsWith('/')) {
		config.url = config.url.slice(1)
	}
  const authStore = useAuthStore()
  if (authStore.token) {
    config.headers.Authorization = `Bearer ${authStore.token}`
  }
  // 本标签页标识：服务端据此在广播「数据变了」时跳过发起者自己（见 utils/client-id.ts）。
  const id = clientID()
  if (id) config.headers['X-Client-Id'] = id
  return config
})

// 401 是否属于「预期分支，不要当成登录失效」。axios v1 的 config.headers 是
// AxiosHeaders 实例，直接按原大小写取属性不可靠，所以先走它的 get()（大小写
// 敏感），再退回普通对象取值。取不到就等于没标记——失败方向是「仍然登出」，
// 与旧行为一致，不会悄悄放过真正的登录失效。
function skipsAuthRedirect(config: any): boolean {
  const headers = config?.headers
  if (!headers) return false
  if (typeof headers.get === 'function') {
    return !!headers.get('X-Skip-Auth-Redirect')
  }
  return !!(headers['X-Skip-Auth-Redirect'] || headers['x-skip-auth-redirect'])
}

request.interceptors.response.use(
  (response) => {
    return response
  },
  (error) => {
    // 退出登录这类幂等请求的 401 是预期分支：会话本来就失效时也必须能退干净，
    // 不该再被强制拽回登录页。
    // 401 是服务端已经拒绝了这个会话：本地直接清干净即可，不必再调
    // /api/auth/logout（那次调用同样会 401）。
    if (error.response?.status === 401 && !skipsAuthRedirect(error.config)) {
      const authStore = useAuthStore()
      authStore.logout()
      router.push('/login')
    }
    return Promise.reject(error)
  }
)

export default request
