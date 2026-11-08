<script>
  import { api } from '../lib/api.js'
  import { bytes, dur } from '../lib/fmt.js'
  import { app, setNode, go, can } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { confirmDialog, promptDialog, toast } from '../lib/ui/overlay.svelte.js'

  let { refreshNodes } = $props()
  let name = $state('')
  let addr = $state('')
  let code = $state('')
  let busy = $state(false)
  let error = $state('')
  let ok = $state('')
  let showHelp = $state(app.nodes.filter((n) => !n.local).length === 0)
  // connection: direct to the agent port, or tunnelled through the server's SSH
  let mode = $state('direct')
  let auth = $state('password')
  let ssh = $state({ host: '', user: '', password: '', key: '', passphrase: '' })
  let editing = $state(null) // node being edited, null = adding

  function reset() {
    name = addr = code = ''
    ssh = { host: '', user: '', password: '', key: '', passphrase: '' }
    mode = 'direct'
    auth = 'password'
    editing = null
  }
  function sshBody() {
    return { host: ssh.host, user: ssh.user, password: auth === 'password' ? ssh.password : '', key: auth === 'key' ? ssh.key : '', passphrase: auth === 'key' ? ssh.passphrase : '' }
  }
  async function submit(e) {
    e.preventDefault()
    busy = true
    error = ''
    ok = ''
    try {
      if (editing) {
        const body = { addr, mode }
        if (mode === 'ssh') body.ssh = sshBody()
        const rec = await api.patch('/api/nodes/' + editing.id, body)
        toast(`Connection to ${rec.name} updated`)
        reset()
      } else {
        const body = { name, addr, code }
        if (mode === 'ssh') body.ssh = sshBody()
        const rec = await api.post('/api/nodes', body)
        ok = `Paired with ${rec.name}${rec.ssh ? ` through SSH ${rec.ssh.user}@${rec.ssh.host}` : ` (${rec.addr})`}.`
        reset()
      }
      await refreshNodes()
    } catch (err) { error = err.message }
    busy = false
  }
  function edit(n) {
    reset()
    editing = n
    addr = n.addr
    if (n.ssh) {
      mode = 'ssh'
      auth = n.ssh.auth
      ssh = { host: n.ssh.host, user: n.ssh.user, password: '', key: '', passphrase: '' }
    }
    error = ok = ''
    document.querySelector('.node-form')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }
  async function rename(n) {
    const v = await promptDialog({ title: 'Rename node', label: 'Display name', value: n.name, icon: 'edit', validate: (x) => (x.trim() ? '' : 'Name is required') })
    if (!v) return
    try { await api.patch('/api/nodes/' + n.id, { name: v }); await refreshNodes(); toast('Node renamed') } catch (e) { toast(e.message, 'err') }
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
            <td class="mono small">{#if n.local}in-process{:else if n.ssh}<div class="row via" title={'SSH host key ' + (n.ssh.hostKey || '')}><Icon name="lock" size={12} /> ssh {n.ssh.user}@{n.ssh.host}</div><div class="dim">→ {n.addr}</div>{:else}{n.addr}{/if}</td>
            <td class="small">{#if n.online}<span style="color:var(--ok)">online</span> · {n.latencyMs} ms · v{n.info.version} · up {dur(n.info.uptimeSec)}{:else}<span style="color:var(--err)" title={n.error}>{(n.error || 'offline').slice(0, 80)}</span>{/if}</td>
            <td>{#if n.online}{#if n.info.docker}<span class="badge ok">yes</span>{:else}<span class="badge" title={n.info.dockerError}>no</span>{/if}{/if}</td>
            <td>{#if n.online}{#if n.info.journal}<span class="badge ok">yes</span>{:else}<span class="badge">no</span>{/if}{/if}</td>
            <td>{#if n.online}{#if n.info.allowWrite}<span class="badge ok">read-write</span>{:else}<span class="badge">read-only</span>{/if}{/if}</td>
            <td class="num">{n.online ? bytes(n.info.totalBytes) : ''}</td>
            <td class="num">
              <button class="sm" onclick={() => { setNode(n.id); go('/sources') }}>Open <Icon name="chevronRight" size={13} /></button>
              {#if !n.local && can('admin')}
                <button class="sm ghost icon-btn" title="Rename" aria-label="Rename" onclick={() => rename(n)}><Icon name="edit" size={14} /></button>
                <button class="sm ghost icon-btn" title="Edit connection" aria-label="Edit connection" onclick={() => edit(n)}><Icon name="server" size={14} /></button>
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
    <form class="card form node-form" onsubmit={submit}>
      <h2>{editing ? `Edit connection · ${editing.name}` : 'Add agent'}</h2>
      <Segmented bind:value={mode} label="Connection" options={[{ value: 'direct', label: 'Direct' }, { value: 'ssh', label: 'Via SSH' }]} />
      {#if mode === 'ssh'}
        <p class="dim small" style="margin:0">The hub logs in to the server over SSH and reaches the agent from there, so no extra port has to be opened.</p>
        <div class="two">
          <label class="field">SSH host (host or host:port, default 22) <input class="mono" bind:value={ssh.host} placeholder="vm-db.lan or 10.0.0.12:2222" required /></label>
          <label class="field">Username <input class="mono" bind:value={ssh.user} placeholder="strawlog" required autocomplete="off" /></label>
        </div>
        <Segmented bind:value={auth} size="sm" label="SSH authentication" options={[{ value: 'password', label: 'Password' }, { value: 'key', label: 'Private key' }]} />
        {#if auth === 'password'}
          <label class="field">Password <input type="password" bind:value={ssh.password} placeholder={editing?.ssh?.auth === 'password' ? 'leave empty to keep the saved one' : ''} required={!(editing?.ssh?.auth === 'password')} autocomplete="new-password" /></label>
        {:else}
          <label class="field">Private key <textarea class="mono" rows="4" bind:value={ssh.key} placeholder={editing?.ssh?.auth === 'key' ? 'leave empty to keep the saved key' : '-----BEGIN OPENSSH PRIVATE KEY-----'} required={!(editing?.ssh?.auth === 'key')} spellcheck="false"></textarea></label>
          <label class="field">Key passphrase (if the key has one) <input type="password" bind:value={ssh.passphrase} autocomplete="new-password" /></label>
        {/if}
        <label class="field">Agent address on that server <input class="mono" bind:value={addr} placeholder="127.0.0.1:7443" /></label>
      {:else}
        <label class="field">Address (host or host:port, default port 7443) <input class="mono" bind:value={addr} placeholder="10.0.0.12:7443 or vm-db.lan" required /></label>
      {/if}
      {#if !editing}
        <label class="field">Pairing code <input class="mono" bind:value={code} placeholder="slp1.…" required autocomplete="off" /></label>
        <label class="field">Display name (optional) <input bind:value={name} placeholder="defaults to the server hostname" /></label>
      {/if}
      {#if error}<div class="alert err">{error}</div>{/if}
      {#if ok}<div class="alert ok">{ok}</div>{/if}
      <div class="row">
        <button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if} {editing ? 'Test & save' : 'Pair'}</button>
        {#if editing}<button type="button" class="ghost" onclick={reset}>Cancel</button>{:else}<button type="button" class="ghost" onclick={() => (showHelp = !showHelp)}>{showHelp ? 'Hide' : 'Show'} setup steps</button>{/if}
      </div>
      <p class="dim small" style="margin:0">{#if editing}The new settings are tested before they are saved. The agent's pinned certificate stays the same.{:else}The code contains the agent's certificate fingerprint, so the very first connection is already pinned. It is single-use and expires after 24h.{/if}{#if mode === 'ssh'} The server's SSH host key is remembered on first connect and checked every time after. SSH credentials are stored on the hub only and are never sent back to the browser.{/if}</p>
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
          <li><b>Direct:</b> open port <b>7443/tcp</b> only to the hub (firewall / Proxmox / security group).<br />
            <b>Via SSH:</b> open nothing; publish the agent on the server's loopback only (<code>"127.0.0.1:7443:7443"</code> in the compose file) and use any SSH login that can reach it.</li>
          <li>Paste the address (or SSH login) + code here.</li>
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
  .two { display: grid; grid-template-columns: 2fr 1fr; gap: 12px; }
  @media (max-width: 640px) { .two { grid-template-columns: 1fr; } }
  .via { gap: 5px; }
  .steps { padding-left: 18px; margin: 0; display: flex; flex-direction: column; gap: 8px; }
</style>
