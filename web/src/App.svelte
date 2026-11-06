<script>
  import { onMount } from 'svelte'
  import { app, parseHash, setNode, can } from './lib/state.svelte.js'
  import { api } from './lib/api.js'
  import Icon from './lib/ui/Icon.svelte'
  import Select from './lib/ui/Select.svelte'
  import Overlays from './lib/ui/Overlays.svelte'
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
    { section: 'Observe' },
    { path: '/', label: 'Overview', icon: 'overview' },
    { path: '/sources', label: 'Sources', icon: 'sources' },
    { path: '/logs', label: 'Live logs', icon: 'logs' },
    { path: '/search', label: 'Search', icon: 'search' },
    { path: '/health', label: 'Health', icon: 'health' },
    { section: 'Manage' },
    { path: '/policies', label: 'Retention', icon: 'retention' },
    { path: '/nodes', label: 'Nodes', icon: 'nodes' },
    { path: '/notifications', label: 'Notifications', icon: 'bell', role: 'admin' },
    { path: '/users', label: 'Users', icon: 'users', role: 'admin' },
    { path: '/settings', label: 'Settings', icon: 'settings' },
  ]

  const pages = { '/': Overview, '/sources': Sources, '/logs': Logs, '/search': Search, '/health': Health, '/policies': Policies, '/nodes': Nodes, '/settings': Settings, '/users': Users, '/notifications': Notifications }
  let Page = $derived(pages[app.route.path] || Overview)
  let menuOpen = $state(false)

  let nodeOptions = $derived(
    app.nodes.map((n) => ({
      value: n.id,
      label: n.name || n.info?.hostname || n.id,
      hint: n.local ? 'this machine' : n.online ? `${n.addr} · ${n.latencyMs} ms` : 'offline · ' + (n.addr || ''),
      dot: n.online ? 'ok' : 'err',
    })),
  )

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
  let initials = $derived((app.user?.username || '?').slice(0, 2).toUpperCase())
</script>

{#if !app.ready}
  <div class="boot"><span class="spin"></span></div>
{:else if !app.authenticated}
  <Login />
{:else}
  <div class="shell">
    <aside class:open={menuOpen}>
      <div class="brand">
        <svg viewBox="0 0 32 32" width="30" height="30"><rect width="32" height="32" rx="8" fill="var(--accent)" /><path d="M8 10h16M8 16h11M8 22h14" stroke="var(--accent-ink)" stroke-width="3" stroke-linecap="round" /></svg>
        <div class="bt"><b>Straw Log</b><span>v{app.version}</span></div>
      </div>

      <div class="nodesel">
        <div class="lbl">Node</div>
        <Select block value={app.node} options={nodeOptions} onchange={setNode} menuWidth={260} label="Node" />
      </div>

      <nav>
        {#each nav.filter((i) => i.section || !i.role || can(i.role)) as item}
          {#if item.section}
            <div class="section">{item.section}</div>
          {:else}
            <a href={'#' + item.path} class:active={app.route.path === item.path}>
              <Icon name={item.icon} size={17} />
              <span>{item.label}</span>
            </a>
          {/if}
        {/each}
      </nav>

      <div class="me">
        <div class="avatar">{initials}</div>
        <div class="who">
          <b class="trunc">{app.user?.username}</b>
          <span>{app.user?.role}</span>
        </div>
        <button class="ghost icon-btn sm" title="Log out" aria-label="Log out" onclick={logout}><Icon name="logout" size={16} /></button>
      </div>
    </aside>
    {#if menuOpen}<div class="scrim" role="presentation" onclick={() => (menuOpen = false)}></div>{/if}
    <main>
      <button class="ghost icon-btn menu" onclick={() => (menuOpen = !menuOpen)} aria-label="Menu"><Icon name="menu" size={18} /></button>
      {#key app.route.path}
        <Page {refreshNodes} />
      {/key}
    </main>
  </div>
{/if}

<Overlays />

<style>
  .boot { height: 100%; display: grid; place-items: center; color: var(--accent); }
  .shell { display: grid; grid-template-columns: 236px 1fr; height: 100%; }
  aside { background: var(--bg2); border-right: 1px solid var(--border); display: flex; flex-direction: column; padding: 16px 12px 12px; gap: 16px; overflow-y: auto; }
  .brand { display: flex; gap: 10px; align-items: center; padding: 2px 6px; }
  .bt { display: flex; flex-direction: column; line-height: 1.2; }
  .bt b { font-size: 15px; letter-spacing: -.01em; }
  .bt span { font-size: 11px; color: var(--dim); }
  .nodesel { display: flex; flex-direction: column; gap: 6px; padding: 0 2px; }
  .nodesel .lbl, .section { font-size: 10.5px; font-weight: 650; text-transform: uppercase; letter-spacing: .08em; color: var(--dim); padding: 0 4px; }
  .section { padding: 12px 10px 4px; }
  .section:first-child { padding-top: 0; }
  nav { display: flex; flex-direction: column; gap: 1px; }
  nav a { display: flex; align-items: center; gap: 11px; padding: 7px 10px; border-radius: var(--r-sm); color: var(--muted); font-weight: 520; font-size: 13.5px; transition: background .1s, color .1s; }
  nav a:hover { background: var(--hover); color: var(--text); text-decoration: none; }
  nav a.active { background: var(--accent-soft); color: var(--text); }
  nav a.active :global(.icon) { color: var(--accent); }
  .me { margin-top: auto; display: flex; align-items: center; gap: 10px; padding: 10px 6px 2px; border-top: 1px solid var(--border); }
  .avatar { width: 30px; height: 30px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: grid; place-items: center; font-size: 11.5px; font-weight: 700; flex: none; }
  .who { display: flex; flex-direction: column; min-width: 0; flex: 1; line-height: 1.25; }
  .who b { font-size: 13px; }
  .who span { font-size: 11.5px; color: var(--dim); text-transform: capitalize; }
  main { overflow-y: auto; min-width: 0; position: relative; }
  .menu { display: none; }
  .scrim { display: none; }
  @media (max-width: 860px) {
    .shell { grid-template-columns: 1fr; }
    aside { position: fixed; inset: 0 auto 0 0; width: 256px; z-index: 50; transform: translateX(-100%); transition: transform .18s ease; box-shadow: var(--shadow-lg); }
    aside.open { transform: none; }
    .scrim { display: block; position: fixed; inset: 0; z-index: 40; background: rgba(10, 12, 16, .5); backdrop-filter: blur(2px); }
    .menu { display: inline-flex; position: absolute; top: 14px; right: 12px; z-index: 5; }
  }
</style>
