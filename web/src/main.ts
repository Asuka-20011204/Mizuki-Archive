import { createApp } from 'vue'
import App from './App.vue'
import './styles/base.css'
import './styles/login.css'
import './styles/library.css'
import './styles/motion.css'
import './styles/responsive.css'

// 浏览器入口只负责挂载根视图；页面状态与交互在 App.vue，HTTP 调用在 api.ts。
createApp(App).mount('#app')
