<script>
  import { api } from '../lib/api.js'
  import { bytes, dur } from '../lib/fmt.js'
  import { app, setNode, go, can } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import { confirmDialog, promptDialog, toast } from '../lib/ui/overlay.svelte.js'

  let { refreshNodes } = $props()
  let name = $state('')
  let addr = $state('')
  let code = $state('')
  let busy = $state(false)
  let error = $state('')
  let ok = $state('')
  let showHelp = $state(app.nodes.filter((n) => !n.local).length === 0)

  async function add(e) {
    e.preventDefault()
    busy = true
    error = ''
    ok = ''
    try {
      const rec = await api.post('/api/nodes', { name, addr, code })
      ok = `Paired with ${rec.name} (${rec.addr}).`
      name = addr = code = ''
      await refreshNodes()
    } catch (err) { error = err.message }
    busy = false
  }
  async function rename(n) {
    const v = await promptDialog({ title: 'Rename node', label: 'Display name', value: n.name, icon: 'edit', validate: (x) => (x.trim() ? '' : 'Name is required') })
    if (!v) return
    try { await api.patch('/api/nodes/' + n.id, { name: v }); await refreshNodes(); toast('Node renamed') } catch (e) { toast(e.message, 'err') }
  }
  async function readdr(n) {
    const v = await promptDialog({ title: 'Change agent address', message: 'Use this when the server got a new IP. The pinned certificate stays the same.', label: 'Address (host:port)', value: n.addr, icon: 'server', mono: true })
    if (!v) return
    try { await api.patch('/api/nodes/' + n.id, { addr: v }); await refreshNodes(); toast('Address updated') } catch (e) { toast(e.message, 'err') }
  }
  async function remove(n) {
    const ok = await confirmDialog({ title: `Remove ${n.name}?`, message: 'The hub forgets this agent. To also revoke this hub on the server, run `strawlog agent unpair` there.', confirmLabel: 'Remove node', danger: true })
    if (!ok) return
    try { await api.del('/api/nodes/' + n.id); await refreshNodes(); toast(`${n.name} removed`) } catch (e) { toast(e.message, 'err') }
  }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Nodes</h1><p class="sub">This machine plus every server running a Straw Log agent. The hub connects to agents; agents never need to reach the hub.</p></div>
    <button onclick={refreshNodes}>Refresh</button>
  </div>

  <div class="card tight" style="margin-bottom:16px">
    <div style="overflow-x:auto">
    <table>
      <thead><tr><th>Node</th><th>Address</th><th>Status</th><th>Docker</th><th>Journal</th><th>Mode</th><th class="num">Logs</th><th></th></tr></thead>
      <tbody>
        {#each app.nodes as n (n.id)}
          <tr>
            <td><div class="row" style="gap:8px"><span class="dot" class:ok={n.online} class:err={!n.online}></span><b>{n.name || n.info?.hostname}</b>{#if n.local}<span class="badge">this machine</span>{/if}</div>
              {#if n.fingerprint}<div class="dim mono small fp" title="Pinned agent certificate (SHA-256 prefix)"><Icon name="lock" size={12} /> {n.fingerprint}</div>{/if}</td>
            <td class="mono small">{n.local ? 'in-process' : n.addr}</td>
            <td class="small">{#if n.online}<span style="color:var(--ok)">online</span> · {n.latencyMs} ms · v{n.info.version} · up {dur(n.info.uptimeSec)}{:else}<span style="color:var(--err)" title={n.error}>{(n.error || 'offline').slice(0, 80)}</span>{/if}</td>
            <td>{#if n.online}{#if n.info.docker}<span class="badge ok">yes</span>{:else}<span class="badge" title={n.info.dockerError}>no</span>{/if}{/if}</td>
            <td>{#if n.online}{#if n.info.journal}<span class="badge ok">yes</span>{:else}<span class="badge">no</span>{/if}{/if}</td>
            <td>{#if n.online}{#if n.info.allowWrite}<span class="badge ok">read-write</span>{:else}<span class="badge">read-only</span>{/if}{/if}</td>
            <td class="num">{n.online ? bytes(n.info.totalBytes) : ''}</td>
            <td class="num">
              <button class="sm" onclick={() => { setNode(n.id); go('/sources') }}>Open <Icon name="chevronRight" size={13} /></button>
              {#if !n.local && can('admin')}
                <button class="sm ghost icon-btn" title="Rename" aria-label="Rename" onclick={() => rename(n)}><Icon name="edit" size={14} /></button>
                <button class="sm ghost icon-btn" title="Change address" aria-label="Change address" onclick={() => readdr(n)}><Icon name="server" size={14} /></button>
                <button class="sm ghost icon-btn danger" title="Remove" aria-label="Remove" onclick={() => remove(n)}><Icon name="trash" size={14} /></button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    </div>
  </div>

  {#if can('admin')}
  <div class="grid cols-2">
    <form class="card form" onsubmit={add}>
      <h2>Add agent</h2>
      <label class="field">Address (host or host:port, default port 7443) <input class="mono" bind:value={addr} placeholder="10.0.0.12:7443 or vm-db.lan" required /></label>
      <label class="field">Pairing code <input class="mono" bind:value={code} placeholder="slp1.…" required autocomplete="off" /></label>
      <label class="field">Display name (optional) <input bind:value={name} placeholder="defaults to the server hostname" /></label>
      {#if error}<div class="alert err">{error}</div>{/if}
      {#if ok}<div class="alert ok">{ok}</div>{/if}
      <div class="row"><button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if} Pair</button><button type="button" class="ghost" onclick={() => (showHelp = !showHelp)}>{showHelp ? 'Hide' : 'Show'} setup steps</button></div>
      <p class="dim small" style="margin:0">The code contains the agent's certificate fingerprint, so the very first connection is already pinned. It is single-use and expires after 24h.</p>
    </form>

    {#if showHelp}
      <div class="card">
        <h2>Install an agent on a server / VM</h2>
        <ol class="steps">
          <li>Copy <code>deploy/agent/docker-compose.yml</code> to the server and set <code>STRAWLOG_ALLOW_FROM</code> to this hub's IP (or your LAN, e.g. <code>192.168.1.0/24</code>).</li>
          <li>Start it and read the pairing code:
            <pre>docker compose up -d
docker compose logs strawlog-agent | grep -A1 "PAIRING CODE"
# or create a new code at any time:
docker compose exec strawlog-agent strawlog agent pair</pre></li>
          <li>Open port <b>7443/tcp</b> only to the hub (firewall / Proxmox / security group).</li>
          <li>Paste address + code here.</li>
        </ol>
        <p class="dim small">Want cleanup (retention) on that server? Set <code>STRAWLOG_ALLOW_WRITE=true</code> and switch the log mounts to <code>:rw</code> — see the comments in the compose file.</p>
      </div>
    {/if}
  </div>
  {/if}
</div>

<style>
  .form { display: flex; flex-direction: column; gap: 12px; }
  .small { font-size: 12px; }
  .fp { display: flex; align-items: center; gap: 5px; margin-top: 2px; padding-left: 16px; }
  .steps { padding-left: 18px; margin: 0; display: flex; flex-direction: column; gap: 8px; }
</style>
