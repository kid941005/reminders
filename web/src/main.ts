import { createApp } from 'vue'
import { createPinia } from 'pinia'
import router from './router'
import App from './App.vue'
import './style.css'
import { dismissBootSplash } from './utils/boot'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

// 首个路由（含全局守卫里的启动引导请求）落定后才收起首屏骨架：在那之前
// router-view 还是空的，过早移除等于把白屏还回去。
void router.isReady().then(dismissBootSplash)
