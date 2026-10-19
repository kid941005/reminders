import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/login',
      name: 'Login',
      component: () => import('../views/LoginView.vue'),
      meta: { requiresAuth: false }
    },
    {
      path: '/register',
      name: 'Register',
      component: () => import('../views/RegisterView.vue'),
      meta: { requiresAuth: false }
    },
    {
      path: '/forgot-password',
      name: 'ForgotPassword',
      component: () => import('../views/ForgotPasswordView.vue'),
      meta: { requiresAuth: false }
    },
    {
      // 后台路由统一前缀为 /admin，需要登录。
      path: '/admin',
      component: () => import('../layouts/MainLayout.vue'),
      meta: { requiresAuth: true },
      children: [
        { path: '', name: 'Home', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'today', title: '今天' } },
        { path: 'planned', name: 'Planned', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'planned', title: '计划' } },
        // 周/月视图共用 CalendarView，靠路由名区分渲染形态。
        { path: 'week', name: 'WeekView', component: () => import('../views/CalendarView.vue'), meta: { reminderView: 'week', title: '本周' } },
        { path: 'month', name: 'MonthView', component: () => import('../views/CalendarView.vue'), meta: { reminderView: 'month', title: '本月' } },
        // 日历里点某一天落到这里：复用 HomeView 的完整列表与编辑能力。
        { path: 'day/:date(\\d{4}-\\d{2}-\\d{2})', name: 'DayView', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'day', title: '某一天' } },
        { path: 'all', name: 'AllReminders', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'all', title: '全部' } },
        { path: 'completed', name: 'Completed', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'completed', title: '已完成' } },
        { path: 'list/:id', name: 'ReminderList', component: () => import('../views/HomeView.vue'), meta: { reminderView: 'all', title: '我的清单' } },
        { path: 'countdowns', name: 'Countdowns', component: () => import('../views/CountdownsView.vue'), meta: { title: '倒数纪念日' } },
        { path: 'notifications', name: 'Notifications', component: () => import('../views/NotificationsView.vue'), meta: { title: '通知中心' } },
        { path: 'channels', name: 'Channels', component: () => import('../views/ChannelsView.vue'), meta: { title: '通知方式' } },
        { path: 'profile', name: 'Profile', component: () => import('../views/ProfileView.vue') },
        { path: 'settings', name: 'Settings', component: () => import('../views/SettingsView.vue') },
        { path: 'users', name: 'AdminUsers', component: () => import('../views/AdminUsersView.vue'), meta: { requiresAdmin: true } },
        { path: 'configs', name: 'AdminConfigs', component: () => import('../views/AdminConfigView.vue'), meta: { requiresAdmin: true } },
        // Provider credentials belong to “通知方式”; retain old URLs as a safe redirect.
        { path: 'providers', redirect: '/admin/channels' },
        { path: 'audit', name: 'AdminAudit', component: () => import('../views/AdminAuditView.vue'), meta: { requiresAdmin: true } },
      ]
    },
    {
      // 后期 / 用于免登录的门户或前端页面，当前先重定向到后台首页。
      path: '/',
      redirect: '/admin',
      meta: { requiresAuth: false }
    },
    {
      // 兜底路由：部署在子路径下时，入口可能落在不带末尾斜杠的前缀上
      //（如 /app/kid941005-reminders），vue-router 剥离 base 后得到的
      // 路径匹配不到任何路由，router-view 会渲染成一片空白（手机端白屏）。
      // 一律送回首页，未登录时由全局守卫转入登录页。
      path: '/:pathMatch(.*)*',
      redirect: '/',
      meta: { requiresAuth: false }
    }
  ]
})

router.beforeEach(async (to, from, next) => {
  const authStore = useAuthStore()
  await authStore.init()

  if (authStore.setupRequired && to.name !== 'Register') {
    // 首次安装强制走注册页创建管理员账号。
    next({ name: 'Register' })
    return
  }

  if (to.meta.requiresAuth !== false && !authStore.isAuthenticated && authStore.requireLogin) {
    next({ name: 'Login' })
  } else if (to.meta.requiresAdmin && !authStore.isAdmin) {
    next({ name: 'Home' })
  } else {
    next()
  }
})

export default router
