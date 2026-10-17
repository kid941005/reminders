// 本地存储键统一加应用前缀。
//
// 同一个域上可能还跑着别的应用（反向代理把多个服务挂在同一域名下的场景），
// 同域即共享同一 localStorage/sessionStorage：裸键名会互相覆盖，对方的
// token 会顶掉本应用的登录态。加前缀后各写各的键，互不干扰。
const P = 'reminders.'

export const SK = {
  token: `${P}token`,
  // 安全提示的「稍后再说」时间戳：同样带前缀，避免与同域其它应用互相覆盖。
  securityPromptDismissed: `${P}security_prompt_dismissed_at`,
  // 本标签页的客户端标识（sessionStorage，一个标签页一个）：随请求与 SSE 连接
  // 一起发给服务端，服务端广播数据变更时据此跳过发起者自己。同样必须带前缀。
  clientId: `${P}client_id`,
} as const
