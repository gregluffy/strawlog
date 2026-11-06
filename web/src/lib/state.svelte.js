function load(k, d) { try { return localStorage.getItem(k) ?? d } catch { return d } }
export function save(k, v) { try { localStorage.setItem(k, v) } catch {} }

export const app = $state({
  ready: false,
  authenticated: false,
  setupRequired: false,
  version: '',
  user: null,
  nodes: [],
  node: load('strawlog.node', 'local'),
  route: { path: '/', params: {} },
})

export function setNode(id) {
  app.node = id
  save('strawlog.node', id)
}

export function parseHash() {
  const h = location.hash.replace(/^#/, '') || '/'
  const [path, qs] = h.split('?')
  const params = Object.fromEntries(new URLSearchParams(qs || ''))
  app.route = { path: path || '/', params }
  if (params.node) setNode(params.node)
}

export function go(path, params = {}) {
  const qs = new URLSearchParams(params).toString()
  location.hash = '#' + path + (qs ? '?' + qs : '')
}

const rank = { viewer: 1, operator: 2, admin: 3 }
export function can(role) {
  return (rank[app.user?.role] || 0) >= rank[role]
}

export function nodeName(id) {
  const n = app.nodes.find((x) => x.id === id)
  return n ? n.name || n.info?.hostname || id : id
}
