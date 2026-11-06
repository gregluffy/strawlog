<script>
  import { onMount } from 'svelte'
  import { app, parseHash, setNode, go, can } from './lib/state.svelte.js'
  import { api } from './lib/api.js'
  import Login from './pages/Login.svelte'
  import Overview from './pages/Overview.svelte'
  import Sources from './pages/Sources.svelte'
  import Logs from './pages/Logs.svelte'
  import Search from './pages/Search.svelte'
  import Health from './pages/Health.svelte'
  import Policies from './pages/Policies.svelte'
  import Nodes from './pages/Nodes.svelte'
  import Settings from './pages/Settings.svelte'
  import Users from './pages/Users.svelte'
  import Notifications from './pages/Notifications.svelte'

  const nav = [
    { path: '/', label: 'Overview', icon: 'M3 12h4l3-8 4 16 3-8h4' },
    { path: '/sources', label: 'Sources', icon: 'M4 6h16M4 12h16M4 18h10' },
    { path: '/logs', label: 'Live logs', icon: 'M5 4h14v16H5zM8 8h8M8 12h8M8 16h5' },
    { path: '/search', label: 'Search', icon: 'M11 4a7 7 0 1 1 0 14 7 7 0 0 1 0-14zM16 16l4 4' },
    { path: '/health', label: 'Health', icon: 'M12 21s-7-4.5-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 11c0 5.5-7 10-7 10z' },
    { path: '/policies', label: 'Retention', icon: 'M12 3v4M12 17v4M5 12H3M21 12h-2M7 7l-1.5-1.5M18.5 18.5 17 17M7 17l-1.5 1.5M18.5 5.5 17 7M12 8a4 4 0 1 1 0 8 4 4 0 0 1 0-8z' },
    { path: '/nodes', label: 'Nodes', icon: 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01' },
    { path: '/notifications', label: 'Notifications', icon: 'M6 16V11a6 6 0 1 1 12 0v5l2 2H4zM10 20a2 2 0 0 0 4 0', role: 'admin' },
    { path: '/users', label: 'Users', icon: 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM2 21v-1a6 6 0 0 1 12 0v1M17 11a3 3 0 1 0 0-6M22 21v-1a5 5 0 0 0-4-4.9', role: 'admin' },
    { path: '/settings', label: 'Settings', icon: 'M4 7h10M18 7h2M4 17h4M12 17h8M14 5v4M8 15v4' },
  ]

  const pages = { '/': Overview, '/sources': Sources, '/logs': Logs, '/search': Search, '/health': Health, '/policies': Policies, '/nodes': Nodes, '/settings': Settings, '/users': Users, '/notifications': Notifications }
  let Page = $derived(pages[app.route.path] || Overview)
  let menuOpen = $state(false)

  async function session() {
    try {
      const s = await api.get('/api/session')
      app.authenticated = s.authenticated
      app.setupRequired = s.setupRequired
      app.version = s.version
      app.user = s.user || null
    } catch {}
    app.ready = true
  }

  async function refreshNodes() {
    try {
      app.nodes = await api.get('/api/nodes')
      if (!app.nodes.find((n) => n.id === app.node) && app.nodes.length) setNode(app.nodes[0].id)
    } catch {}
  }

  $effect(() => {
    if (app.authenticated) refreshNodes()
  })

  onMount(() => {
    parseHash()
    window.addEventListener('hashchange', () => { parseHash(); menuOpen = false })
    session()
    const t = setInterval(() => app.authenticated && refreshNodes(), 30000)
    return () => clearInterval(t)
  })

  async function logout() {
    await api.post('/api/logout').catch(() => {})
    app.authenticated = false
    app.user = null
  }
</script>

{#if !app.ready}
  <div class="empty"><span class="spin"></span></div>
{:else if !app.authenticated}
  <Login />
{:else}
  <div class="shell">
    <aside class:open={menuOpen}>
      <div class="brand">
        <svg viewBox="0 0 32 32" width="26" height="26"><rect width="32" height="32" rx="7" fill="var(--accent)" /><path d="M8 10h16M8 16h11M8 22h14" stroke="var(--accent-ink)" stroke-width="3" stroke-linecap="round" /></svg>
        <div><b>Straw Log</b><div class="dim ver">v{app.version}</div></div>
      </div>
      <label class="nodesel">
        <span class="dim">Node</span>
        <select value={app.node} onchange={(e) => setNode(e.currentTarget.value)}>
          {#each app.nodes as n (n.id)}
            <option value={n.id}>{n.online ? '●' : '○'} {n.name || n.info?.hostname || n.id}{n.local ? ' (this machine)' : ''}</option>
          {/each}
        </select>
      </label>
      <nav>
        {#each nav.filter((i) => !i.role || can(i.role)) as item}
          <a href={'#' + item.path} class:active={app.route.path === item.path}>
            <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d={item.icon} /></svg>
            {item.label}
          </a>
        {/each}
      </nav>
      <div class="me">
        <div class="trunc"><b>{app.user?.username}</b> <span class="badge">{app.user?.role}</span></div>
        <button class="ghost sm" onclick={logout}>Log out</button>
      </div>
    </aside>
    <main>
      <button class="ghost menu" onclick={() => (menuOpen = !menuOpen)} aria-label="Menu">☰</button>
      {#key app.route.path}
        <Page {refreshNodes} />
      {/key}
    </main>
  </div>
{/if}

<style>
  .shell { display: grid; grid-template-columns: 220px 1fr; height: 100%; }
  aside { background: var(--bg2); border-right: 1px solid var(--border); display: flex; flex-direction: column; padding: 16px 12px; gap: 14px; overflow-y: auto; }
  .brand { display: flex; gap: 10px; align-items: center; padding: 2px 6px 6px; }
  .ver { font-size: 11px; }
  .nodesel { display: flex; flex-direction: column; gap: 4px; font-size: 11.5px; padding: 0 4px; }
  .nodesel select { width: 100%; }
  nav { display: flex; flex-direction: column; gap: 2px; }
  nav a { display: flex; align-items: center; gap: 10px; padding: 7px 10px; border-radius: 6px; color: var(--muted); }
  nav a:hover { background: var(--panel); color: var(--text); text-decoration: none; }
  nav a.active { background: var(--panel); color: var(--text); box-shadow: inset 2px 0 0 var(--accent); }
  .me { margin-top: auto; display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 6px 0; border-top: 1px solid var(--border); font-size: 13px; }
  main { overflow-y: auto; min-width: 0; position: relative; }
  .menu { display: none; }
  @media (max-width: 860px) {
    .shell { grid-template-columns: 1fr; }
    aside { position: fixed; inset: 0 auto 0 0; width: 240px; z-index: 10; transform: translateX(-100%); transition: transform .15s; }
    aside.open { transform: none; }
    .menu { display: inline-flex; position: absolute; top: 10px; right: 10px; z-index: 5; font-size: 18px; }
  }
</style>
