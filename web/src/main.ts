import { createApp } from 'vue'
import App from './App.vue'
import { initTheme } from './theme'
import './styles.css'

// Before mount, so the first paint is already in the right theme rather than
// flashing light and correcting itself.
initTheme()

createApp(App).mount('#app')
