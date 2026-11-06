<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, ago } from '../lib/fmt.js'
  import { app, go, nodeName, can } from '../lib/state.svelte.js'

  let policies = $state([])
  let sources = $state([])
  let info = $state(null)
  let runs = $state([])
  let edit = $state(null) // policy being edited (copy)
  let editIdx = $state(-1)
  let plan = $state(null)
  let busy = $state('')
  let error = $state('')
  let msg = $state('')

  let apps = $derived([...new Set(sources.map((s) => s.app))].sort())
  let srcName = $derived(Object.fromEntries(sources.map((s) => [s.id, `${s.app} / ${s.name}`])))

  async function load() {
    error = ''
    try {
      ;[policies, sources, info, runs] = await Promise.all([
        api.get(api.n(app.node, '/policies')),
        api.get(api.n(app.node, '/sources')),
        api.get(api.n(app.node, '/info')),
        api.get(api.n(app.node, '/runs')),
      ])
    } catch (e) { error = e.message }
    const sid = app.route.params.source
    if (sid && !edit) {
      const s = sources.find((x) => x.id === sid)
      if (s) newPolicy(s)
    }
  }
  onMount(load)
  let lastNode = app.node
  $effect(() => { if (app.node !== lastNode) { lastNode = app.node; edit = null; plan = null; load() } })

  function blank() {
    return { id: '', name: '', enabled: true, match: { apps: [], kinds: [], sources: [], pathGlob: '' }, maxAgeDays: 0, maxRotated: 0, compressRotated: false, maxActiveMB: 0, archiveBeforeTruncate: true, maxTotalMB: 0 }
  }
  function newPolicy(s) {
    const p = blank()
    if (s) {
      p.name = `${s.name} retention`
      p.match.sources = [s.id]
      if (s.kind === 'journal') p.maxTotalMB = 1024
      else { p.maxActiveMB = 200; p.maxRotated = 5; p.maxAgeDays = 14; p.compressRotated = s.kind === 'file' }
    }
    edit = p
    editIdx = -1
    plan = null
  }
  function editPolicy(i) {
    edit = JSON.parse(JSON.stringify(policies[i]))
    edit.match = { apps: [], kinds: [], sources: [], pathGlob: '', ...edit.match }
    editIdx = i
    plan = null
  }
  function clean(p) {
    const c = JSON.parse(JSON.stringify(p))
    for (const k of ['maxAgeDays', 'maxRotated', 'maxActiveMB', 'maxTotalMB']) c[k] = Number(c[k]) || 0
    return c
  }

  async function saveAll(list, note) {
    busy = 'save'
    error = ''
    try {
      policies = await api.put(api.n(app.node, '/policies'), list.map(clean))
      msg = note
      setTimeout(() => (msg = ''), 3000)
      return true
    } catch (e) { error = e.message; return false } finally { busy = '' }
  }
  async function save() {
    const list = [...policies]
    if (editIdx >= 0) list[editIdx] = edit
    else list.push(edit)
    if (await saveAll(list, 'Saved. The agent enforces enabled policies on its own schedule.')) { edit = null; plan = null }
  }
  async function remove(i) {
    if (!confirm(`Delete policy "${policies[i].name}"?`)) return
    await saveAll(policies.filter((_, j) => j !== i), 'Deleted.')
    if (editIdx === i) edit = null
  }
  async function toggle(i) {
    const list = JSON.parse(JSON.stringify(policies))
    list[i].enabled = !list[i].enabled
    await saveAll(list, list[i].enabled ? 'Enabled.' : 'Disabled.')
  }
  async function preview(p) {
    busy = 'plan'
    error = ''
    try { plan = await api.post(api.n(app.node, '/plan'), p ? { policy: clean(p) } : {}) } catch (e) { error = e.message }
    busy = ''
  }
  async function apply(ids) {
    const what = ids ? `policy "${policies.find((p) => p.id === ids[0])?.name}"` : 'all enabled policies'
    if (!confirm(`Apply ${what} on ${nodeName(app.node)} now? Files will be deleted/truncated as shown in the preview.`)) return
    busy = 'apply'
    error = ''
    try {
      plan = await api.post(api.n(app.node, '/apply'), ids ? { policyIds: ids } : {})
      runs = await api.get(api.n(app.node, '/runs'))
      sources = await api.get(api.n(app.node, '/sources'))
    } catch (e) { error = e.message }
    busy = ''
  }

  function toggleIn(arr, v) { return arr.includes(v) ? arr.filter((x) => x !== v) : [...arr, v] }
  function describe(p) {
    const m = p.match || {}
    const parts = []
    if (m.apps?.length) parts.push('apps: ' + m.apps.join(', '))
    if (m.kinds?.length) parts.push('kinds: ' + m.kinds.join(', '))
    if (m.sources?.length) parts.push(m.sources.map((s) => srcName[s] || s).join(', '))
    if (m.pathGlob) parts.push('path ' + m.pathGlob)
    return parts.join(' · ') || 'all sources'
  }
  function rules(p) {
    const r = []
    if (p.maxActiveMB) r.push(`active ≤ ${p.maxActiveMB} MB${p.archiveBeforeTruncate ? ' (archive)' : ''}`)
    if (p.maxTotalMB) r.push(`total ≤ ${p.maxTotalMB} MB`)
    if (p.maxRotated) r.push(`keep ${p.maxRotated} rotated`)
    if (p.maxAgeDays) r.push(`delete > ${p.maxAgeDays}d`)
    if (p.compressRotated) r.push('gzip rotated')
    return r.join(' · ')
  }
  const opLabel = { delete: 'delete', compress: 'gzip', truncate: 'truncate', 'archive-truncate': 'archive + truncate', vacuum: 'journal vacuum' }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Retention</h1><p class="sub">Policies live on <b>{nodeName(app.node)}</b> and run there every hour, even when this dashboard is offline.</p></div>
    <div class="row">
      <button onclick={() => preview(null)} disabled={!!busy || !policies.length}>{#if busy === 'plan' && !edit}<span class="spin"></span>{/if} Preview all</button>
      {#if can('operator')}
        <button onclick={() => apply(null)} disabled={!!busy || !info?.allowWrite || !policies.some((p) => p.enabled)}>Apply all now</button>
        <button class="primary" onclick={() => newPolicy(null)}>New policy</button>
      {/if}
    </div>
  </div>

  {#if info && !info.allowWrite}
    <div class="alert warn" style="margin-bottom:12px">
      This node is <b>read-only</b>. You can create policies and preview them, but nothing is deleted until the node runs with
      <code>STRAWLOG_ALLOW_WRITE=true</code> and the log directories are mounted <code>:rw</code>.
    </div>
  {:else if info}
    <div class="dim small" style="margin-bottom:12px">Writable paths: <span class="mono">{(info.writePaths || []).join(', ')}</span></div>
  {/if}
  {#if error}<div class="alert err" style="margin-bottom:12px">{error}</div>{/if}
  {#if msg}<div class="alert ok" style="margin-bottom:12px">{msg}</div>{/if}

  <div class="grid cols-2">
    <div class="card tight">
      {#if policies.length === 0}
        <div class="empty">No policies yet. Start from a source on the <a href="#/sources">Sources</a> page or create one.</div>
      {:else}
        <table>
          <thead><tr><th>Policy</th><th>Rules</th><th></th></tr></thead>
          <tbody>
            {#each policies as p, i (p.id)}
              <tr class:sel={editIdx === i && edit}>
                <td>
                  <label class="check"><input type="checkbox" checked={p.enabled} disabled={!can('operator')} onchange={() => toggle(i)} /> <b>{p.name}</b></label>
                  <div class="dim small trunc" style="max-width:280px">{describe(p)}</div>
                </td>
                <td class="small muted">{rules(p)}</td>
                <td class="num">
                  <button class="sm" onclick={() => editPolicy(i)}>Edit</button>
                  <button class="sm" onclick={() => preview(p)}>Preview</button>
                  {#if can('operator')}
                    <button class="sm" onclick={() => apply([p.id])} disabled={!info?.allowWrite || !!busy}>Run</button>
                    <button class="sm danger" onclick={() => remove(i)}>✕</button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>

    {#if edit}
      <div class="card editor">
        <h2>{editIdx >= 0 ? 'Edit policy' : 'New policy'}</h2>
        <label class="field">Name <input bind:value={edit.name} placeholder="e.g. Keep nginx under 1 GB" /></label>

        <h3>Applies to</h3>
        <div class="chips">
          {#each ['docker', 'file', 'journal'] as k}
            <button type="button" class="chip sm" class:on={edit.match.kinds.includes(k)} onclick={() => (edit.match.kinds = toggleIn(edit.match.kinds, k))}>{k}</button>
          {/each}
        </div>
        <div class="chips">
          {#each apps as a}
            <button type="button" class="chip sm" class:on={edit.match.apps.includes(a)} onclick={() => (edit.match.apps = toggleIn(edit.match.apps, a))}>{a}</button>
          {/each}
        </div>
        <label class="field">Specific sources
          <select onchange={(e) => { if (e.currentTarget.value) edit.match.sources = [...new Set([...edit.match.sources, e.currentTarget.value])]; e.currentTarget.value = '' }}>
            <option value="">+ add source…</option>
            {#each sources as s}<option value={s.id}>{s.app} / {s.name}</option>{/each}
          </select>
        </label>
        {#if edit.match.sources.length}
          <div class="chips">{#each edit.match.sources as s}<button type="button" class="chip sm on" onclick={() => (edit.match.sources = edit.match.sources.filter((x) => x !== s))}>{srcName[s] || s} ✕</button>{/each}</div>
        {/if}
        <label class="field">Path glob (optional) <input class="mono" bind:value={edit.match.pathGlob} placeholder="/var/log/nginx/*.log" /></label>
        <p class="dim small" style="margin:0">All conditions must match. Nothing selected = every source on the node.</p>

        <h3>Rules</h3>
        <div class="grid two">
          <label class="field">Max active file size (MB)<input type="number" min="0" bind:value={edit.maxActiveMB} /></label>
          <label class="field">Max total size incl. rotated (MB)<input type="number" min="0" bind:value={edit.maxTotalMB} /></label>
          <label class="field">Keep at most N rotated files<input type="number" min="0" bind:value={edit.maxRotated} /></label>
          <label class="field">Delete rotated older than (days)<input type="number" min="0" bind:value={edit.maxAgeDays} /></label>
        </div>
        <label class="check"><input type="checkbox" bind:checked={edit.archiveBeforeTruncate} /> Keep a gzip archive before truncating the active file</label>
        <label class="check"><input type="checkbox" bind:checked={edit.compressRotated} /> Gzip uncompressed rotated files (older than 1h)</label>
        <label class="check"><input type="checkbox" bind:checked={edit.enabled} /> Enabled (runs automatically every hour)</label>
        <p class="dim small" style="margin:0">0 = no limit. Active files are truncated in place (same inode), so running apps keep logging. Journald uses <code>journalctl --vacuum-*</code>.</p>

        <div class="row" style="margin-top:6px">
          <button onclick={() => preview(edit)} disabled={!!busy}>{#if busy === 'plan'}<span class="spin"></span>{/if} Preview (dry run)</button>
          {#if can('operator')}<button class="primary" onclick={save} disabled={!!busy || !edit.name}>Save</button>{/if}
          <button class="ghost" onclick={() => { edit = null; plan = null; go('/policies') }}>Cancel</button>
        </div>
      </div>
    {/if}
  </div>

  {#if plan}
    <div class="card tight" style="margin-top:14px">
      <div class="row ph">
        <h2 style="margin:0">{plan.dryRun ? 'Preview' : 'Result'}</h2>
        <span class="badge {plan.dryRun ? 'info' : 'ok'}">{plan.dryRun ? 'dry run — nothing changed' : 'applied'}</span>
        <span class="muted">{plan.actions.length} actions · {plan.dryRun ? 'would free' : 'freed'} <b>{bytes(plan.freedBytes)}</b></span>
        <button class="ghost sm right" onclick={() => (plan = null)}>✕</button>
      </div>
      {#if plan.actions.length === 0}
        <div class="empty">Nothing to do — everything is within limits.</div>
      {:else}
        <div style="overflow-x:auto; max-height:420px">
          <table>
            <thead><tr><th>Action</th><th>File</th><th>Reason</th><th class="num">Bytes</th><th></th></tr></thead>
            <tbody>
              {#each plan.actions as a}
                <tr>
                  <td><span class="badge {a.op === 'delete' || a.op === 'truncate' ? 'err' : 'info'}">{opLabel[a.op] || a.op}</span></td>
                  <td class="mono small trunc" style="max-width:420px" title={a.path}>{a.path}</td>
                  <td class="small muted">{a.reason}<span class="dim"> · {a.policy}</span></td>
                  <td class="num">{bytes(a.bytes)}</td>
                  <td class="small">{#if a.error}<span style="color:var(--err)">{a.error}</span>{:else if a.done}<span style="color:var(--ok)">done</span>{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {/if}

  {#if runs.length}
    <h3 style="margin-top:22px">Recent runs</h3>
    <div class="card tight">
      <table>
        <thead><tr><th>When</th><th>Trigger</th><th class="num">Actions</th><th class="num">Errors</th><th class="num">Freed</th></tr></thead>
        <tbody>
          {#each runs.slice(0, 15) as r}
            <tr class="click" onclick={() => (plan = r)}>
              <td>{ago(r.at)}</td><td>{r.trigger}</td><td class="num">{r.actions.length}</td>
              <td class="num">{r.actions.filter((a) => a.error).length || '–'}</td><td class="num">{bytes(r.freedBytes)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .editor { display: flex; flex-direction: column; gap: 10px; }
  .editor h3 { margin: 8px 0 0; }
  .two { grid-template-columns: 1fr 1fr; gap: 10px; }
  .chips { display: flex; flex-wrap: wrap; gap: 5px; }
  .chip { border-radius: 99px; }
  .chip.on { background: var(--accent); color: var(--accent-ink); border-color: var(--accent); }
  .small { font-size: 12px; }
  .ph { padding: 12px 14px; border-bottom: 1px solid var(--border); }
  tr.sel td { background: var(--panel2); }
</style>
