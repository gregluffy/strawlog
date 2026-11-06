import { app } from './state.svelte.js'

async function req(method, path, body) {
  const opt = { method, headers: { 'X-Strawlog': '1' }, credentials: 'same-origin' }
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json'
    opt.body = JSON.stringify(body)
  }
  const r = await fetch(path, opt)
  let data = null
  const text = await r.text()
  try { data = text ? JSON.parse(text) : null } catch { data = { error: text } }
  if (r.status === 401 && !path.startsWith('/api/login') && !path.startsWith('/api/setup')) {
    app.authenticated = false
  }
  if (!r.ok) throw new Error((data && data.error) || r.statusText)
  return data
}

export const api = {
  get: (p) => req('GET', p),
  post: (p, b) => req('POST', p, b ?? {}),
  put: (p, b) => req('PUT', p, b),
  patch: (p, b) => req('PATCH', p, b),
  del: (p) => req('DELETE', p),
  n: (node, p) => `/api/n/${encodeURIComponent(node)}${p}`,
}
