import request from './request'

// 首屏引导信息：一次请求拿到渲染第一帧所需的全部内容。
//
// 此前是「公开配置 + 初始化状态（并行）+ 令牌校验」两到三次往返，弱网下
// 每多一次往返就是多等一截白屏；合并后首屏只需一个 RTT。
export interface BootstrapAuth {
  authenticated: boolean
  require_login?: string
  user: { id: number; username: string; role: string } | null
}

export interface BootstrapData {
  setup_required: boolean
  configs: Record<string, string>
  auth: BootstrapAuth
}

export function getBootstrap() {
  return request.get('/api/bootstrap', { timeout: 15000 })
}
