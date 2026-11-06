<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { ago } from '../lib/fmt.js'
  import { app } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { confirmDialog, toast } from '../lib/ui/overlay.svelte.js'

  let channels = $state([])
  let events = $state([])
  let publicUrl = $state('')
  let edit = $state(null)
  let error = $state('')
  let busy = $state('')
  let test = $state(null)

  const types = {
    discord: { label: 'Discord', color: '#5865F2', placeholder: 'https://discord.com/api/webhooks/123456789/abcdef…',
      help: 'Discord: Server settings → Integrations → Webhooks → New Webhook → pick the channel → Copy Webhook URL.' },
    teams: { label: 'Microsoft Teams', color: '#6264A7', placeholder: 'https://….webhook.office.com/… or https://….logic.azure.com/workflows/…',
      help: 'Teams: in the channel ⋯ → Workflows → "Post to a channel when a webhook request is received" → copy the URL. (Old "Incoming Webhook" connector URLs work too.)' },
    generic: { label: 'Generic JSON', color: '#8b93a5', placeholder: 'https://n8n.lan/webhook/strawlog',
      help: 'Any HTTP endpoint (n8n, Node-RED, ntfy bridge, your own service). Receives {"source":"strawlog","events":[…]}.' },
  }

  async function load() {
    try {
      const r = await api.get('/api/notifications')
      channels = r.channels
      events = r.events
      publicUrl = r.publicUrl
      error = ''
    } catch (e) { error = e.message }
  }
  onMount(load)

  let groups = $derived(events.reduce((m, e) => ((m[e.group] ||= []).push(e), m), {}))

  function startNew() {
    edit = { id: '', name: '', type: 'discord', url: '', urlHint: '', enabled: true, events: events.filter((e) => e.default).map((e) => e.id), nodes: [] }
    test = null
  }
  function startEdit(c) {
    edit = { ...c, url: '', events: [...c.events], nodes: [...(c.nodes || [])] }
    test = null
  }
  function toggleEvent(id) {
    edit.events = edit.events.includes(id) ? edit.events.filter((x) => x !== id) : [...edit.events, id]
  }
  function toggleNode(id) {
    edit.nodes = edit.nodes.includes(id) ? edit.nodes.filter((x) => x !== id) : [...edit.nodes, id]
  }
  function body(e) {
    return { id: e.id, name: e.name, type: e.type, url: e.url, enabled: e.enabled, events: e.events, nodes: e.nodes }
  }

  async function sendTest(c) {
    busy = 'test:' + (c.id || 'new')
    test = null
    try {
      test = { ...(await api.post('/api/notifications/test', body(c))), for: c.id || 'new' }
    } catch (e) { test = { ok: false, error: e.message, for: c.id || 'new' } }
    busy = ''
    if (!edit) await load()
  }
  async function save() {
    busy = 'save'
    error = ''
    try {
      const r = edit.id ? await api.put('/api/notifications/' + edit.id, body(edit)) : await api.post('/api/notifications', body(edit))
      channels = r.channels
      toast('Channel saved')
      edit = null
    } catch (e) { error = e.message }
    busy = ''
  }
  async function toggle(c) {
    try { channels = (await api.put('/api/notifications/' + c.id, body({ ...c, url: '', enabled: !c.enabled }))).channels } catch (e) { error = e.message }
  }
  async function remove(c) {
    if (!(await confirmDialog({ title: `Delete "${c.name}"?`, message: 'No more notifications will be sent to this webhook.', confirmLabel: 'Delete channel', danger: true }))) return
    try { channels = (await api.del('/api/notifications/' + c.id)).channels; if (edit?.id === c.id) edit = null } catch (e) { error = e.message }
  }
  const label = (id) => events.find((e) => e.id === id)?.label || id
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Notifications</h1><p class="sub">Send alerts to Discord or Microsoft Teams through webhooks. The hub checks all nodes every few minutes and only notifies about <i>changes</i>.</p></div>
    <button class="primary" onclick={startNew}><Icon name="plus" size={15} /> Add channel</button>
  </div>
  {#if error}<div class="alert err" style="margin-bottom:12px">{error}</div>{/if}
  {#if !publicUrl}
    <div class="dim small" style="margin-bottom:12px">Tip: set <code>STRAWLOG_PUBLIC_URL</code> on the hub to add an "Open Straw Log" link to every message.</div>
  {/if}

  <div class="grid cols-2">
    <div class="list">
      {#if channels.length === 0}
        <div class="card empty"><div class="ei"><Icon name="bell" size={22} /></div>No channels yet.<span class="dim small">Add a Discord or Teams webhook to get alerts.</span></div>
      {/if}
      {#each channels as c (c.id)}
        <div class="card ch" class:sel={edit?.id === c.id}>
          <div class="row">
            <span class="tbadge" style="background:{types[c.type]?.color}">{types[c.type]?.label}</span>
            <b class="grow trunc">{c.name}</b>
            <Switch checked={c.enabled} onchange={() => toggle(c)} size="sm" title={c.enabled ? 'Enabled' : 'Disabled'} />
          </div>
          <div class="mono dim small trunc">{c.urlHint}</div>
          <div class="chips">
            {#each c.events as e}<span class="badge">{label(e)}</span>{/each}
          </div>
          <div class="small muted">
            {c.nodes?.length ? 'Nodes: ' + c.nodes.map((id) => app.nodes.find((n) => n.id === id)?.name || id).join(', ') : 'All nodes'}
            {#if c.last} · last delivery {ago(c.last.at)}: {#if c.last.ok}<span style="color:var(--ok)">ok</span>{:else}<span style="color:var(--err)">{c.last.error}</span>{/if}{/if}
          </div>
          {#if test && test.for === c.id && !edit}
            <div class="alert {test.ok ? 'ok' : 'err'} small"><Icon name={test.ok ? 'ok' : 'alert'} size={15} /><span>{test.ok ? 'Test message delivered.' : 'Test failed: ' + test.error}</span></div>
          {/if}
          <div class="row">
            <button class="sm" onclick={() => sendTest(c)} disabled={!!busy}>{#if busy === 'test:' + c.id}<span class="spin"></span>{:else}<Icon name="send" size={13} />{/if} Send test</button>
            <button class="sm" onclick={() => startEdit(c)}><Icon name="edit" size={13} /> Edit</button>
            <button class="sm ghost icon-btn danger right" title="Delete" aria-label="Delete channel" onclick={() => remove(c)}><Icon name="trash" size={14} /></button>
          </div>
        </div>
      {/each}
    </div>

    {#if edit}
      <div class="card editor">
        <h2>{edit.id ? 'Edit channel' : 'New channel'}</h2>
        <Segmented block value={edit.type} options={Object.entries(types).map(([value, t]) => ({ value, label: t.label }))} onchange={(v) => { edit.type = v; edit.url = '' }} label="Channel type" />
        <label class="field">Name <input bind:value={edit.name} placeholder="e.g. #ops-alerts" /></label>
        <label class="field">Webhook URL
          <input class="mono" type="password" bind:value={edit.url} autocomplete="off" placeholder={edit.id ? `unchanged (${edit.urlHint})` : types[edit.type].placeholder} />
        </label>
        <p class="dim small hint">{types[edit.type].help} The URL is a secret: it is stored only on the hub and never shown again.</p>

        <h3>Notify me when…</h3>
        {#each Object.entries(groups) as [g, list]}
          <div class="egroup">
            <div class="gname">{g}</div>
            {#each list as e}
              <label class="ev" class:on={edit.events.includes(e.id)}>
                <input type="checkbox" checked={edit.events.includes(e.id)} onchange={() => toggleEvent(e.id)} />
                <div><b>{e.label}</b><div class="muted small">{e.description}</div></div>
              </label>
            {/each}
          </div>
        {/each}

        <h3>Nodes</h3>
        <div class="chips">
          <button type="button" class="chip" class:on={edit.nodes.length === 0} onclick={() => (edit.nodes = [])}>All nodes</button>
          {#each app.nodes as n}
            <button type="button" class="chip" class:on={edit.nodes.includes(n.id)} onclick={() => toggleNode(n.id)}>{n.name || n.info?.hostname || n.id}</button>
          {/each}
        </div>
        <Switch bind:checked={edit.enabled} label="Enabled" />

        {#if test && test.for === (edit.id || 'new')}
          <div class="alert {test.ok ? 'ok' : 'err'}"><Icon name={test.ok ? 'ok' : 'alert'} size={16} /><span>{test.ok ? 'Test message delivered — check the channel.' : 'Test failed: ' + test.error}</span></div>
        {/if}
        <div class="row">
          <button onclick={() => sendTest(edit)} disabled={!!busy || (!edit.url && !edit.id)}>{#if busy.startsWith('test')}<span class="spin"></span>{:else}<Icon name="send" size={14} />{/if} Send test</button>
          <button class="primary" onclick={save} disabled={!!busy || !edit.name || !edit.events.length || (!edit.url && !edit.id)}>Save</button>
          <button class="ghost" onclick={() => (edit = null)}>Cancel</button>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .list { display: flex; flex-direction: column; gap: 10px; }
  .ch { display: flex; flex-direction: column; gap: 8px; }
  .ch.sel { border-color: var(--accent); }
  .tbadge { color: #fff; font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 99px; white-space: nowrap; }
  .small { font-size: 12px; }
  .editor { display: flex; flex-direction: column; gap: 10px; }
  .editor h3 { margin: 10px 0 0; }
  .hint { margin: -4px 0 0; }
  .egroup { display: flex; flex-direction: column; gap: 4px; }
  .gname { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); margin-top: 4px; }
  .ev { display: flex; gap: 11px; align-items: flex-start; padding: 9px 12px; border: 1px solid var(--border); border-radius: var(--r-md); transition: border-color .12s, background .12s; cursor: pointer; font-size: 13px; }
  .ev.on { border-color: color-mix(in srgb, var(--accent) 60%, var(--border)); background: color-mix(in srgb, var(--accent) 7%, transparent); }
  .ev input { margin-top: 3px; }
</style>
