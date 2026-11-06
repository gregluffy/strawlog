import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'

try {
  const t = localStorage.getItem('strawlog.theme')
  if (t && t !== 'auto') document.documentElement.setAttribute('data-theme', t)
} catch {}

export default mount(App, { target: document.getElementById('app') })
