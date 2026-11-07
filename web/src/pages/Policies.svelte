<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, ago, until, every } from '../lib/fmt.js'
  import { app, go, nodeName, can } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Select from '../lib/ui/Select.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { confirmDialog, toast } from '../lib/ui/overlay.svelte.js'

  let policies = $state([])
  let sources = $state([])
  let info = $state(null)
  let runs = $state([])
  let edit = $state(null) // policy being edited (copy)
  let editIdx = $state(-1)
  let plan = $state(null)
  let busy = $state('')
  let error = $state('')

  let apps = $derived([...new Set(sources.map((s) => s.app))].sort())
  const presets = [15, 30, 60, 120, 180, 360, 720]
  const schedDefaults = { mode: 'interval', everyMin: 60, at: '03:00', weekday: 0, day: 1 }
  const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const weekdayOpts = [1, 2, 3, 4, 5, 6, 0].map((d) => ({ value: d, label: weekdays[d] }))
  const ordinal = (n) => n + (n % 10 === 1 && n !== 11 ? 'st' : n % 10 === 2 && n !== 12 ? 'nd' : n % 10 === 3 && n !== 13 ? 'rd' : 'th')
  const dayOpts = Array.from({ length: 31 }, (_, i) => ({ value: i + 1, label: ordinal(i + 1), hint: i >= 28 ? 'or last day' : undefined }))
  const modeOpts = [
    { value: 'interval', label: 'Repeat' },
    { value: 'daily', label: 'Daily' },
    { value: 'weekly', label: 'Weekly' },
    { value: 'monthly', label: 'Monthly' },
  ]
  let intervalOpts = $derived(
    [...new Set([...presets, ...(edit?.schedule?.everyMin ? [edit.schedule.everyMin] : [])])]
      .sort((a, b) => a - b)
      .map((m) => ({ value: m, label: every(m).replace(/^./, (c) => c.toUpperCase()) })),
  )
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
    return { id: '', name: '', enabled: true, schedule: { ...schedDefaults }, match: { apps: [], kinds: [], sources: [], pathGlob: '' }, maxAgeDays: 0, maxRotated: 0, compressRotated: false, maxActiveMB: 0, archiveBeforeTruncate: true, maxTotalMB: 0 }
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
    edit.schedule = { ...schedDefaults, ...edit.schedule }
    for (const k of ['everyMin', 'at', 'day']) if (!edit.schedule[k]) edit.schedule[k] = schedDefaults[k]
    editIdx = i
    plan = null
  }
  function clean(p) {
    const c = JSON.parse(JSON.stringify(p))
    for (const k of ['maxAgeDays', 'maxRotated', 'maxActiveMB', 'maxTotalMB']) c[k] = Number(c[k]) || 0
    delete c.lastRun
    delete c.nextRun
    const sc = c.schedule || {}
    const at = sc.at || '03:00'
    c.schedule =
      sc.mode === 'daily' ? { mode: 'daily', at }
      : sc.mode === 'weekly' ? { mode: 'weekly', at, weekday: Number(sc.weekday) || 0 }
      : sc.mode === 'monthly' ? { mode: 'monthly', at, day: Number(sc.day) || 1 }
      : { mode: 'interval', everyMin: Number(sc.everyMin) || 60 }
    return c
  }

  async function saveAll(list, note) {
    busy = 'save'
    error = ''
    try {
      policies = await api.put(api.n(app.node, '/policies'), list.map(clean))
      toast(note)
      return true
    } catch (e) { error = e.message; return false } finally { busy = '' }
  }
  async function save() {
    const list = [...policies]
    if (editIdx >= 0) list[editIdx] = edit
    else list.push(edit)
    const note = edit.enabled ? `Saved. Runs ${schedText(edit.schedule)} on ${nodeName(app.node)}.` : 'Saved as disabled. It only runs when you start it.'
    if (await saveAll(list, note)) { edit = null; plan = null }
  }
  async function remove(i) {
    if (!(await confirmDialog({ title: 'Delete policy?', message: `"${policies[i].name}" will stop running on ${nodeName(app.node)}. Files are not touched.`, confirmLabel: 'Delete', danger: true }))) return
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
    if (!(await confirmDialog({ title: 'Run retention now?', message: `Apply ${what} on ${nodeName(app.node)}. Files will be deleted, compressed or truncated as shown in the preview. This can't be undone.`, confirmLabel: 'Run now', danger: true }))) return
    busy = 'apply'
    error = ''
    try {
      plan = await api.post(api.n(app.node, '/apply'), ids ? { policyIds: ids } : {})
      runs = await api.get(api.n(app.node, '/runs'))
      sources = await api.get(api.n(app.node, '/sources'))
      toast(`Freed ${bytes(plan.freedBytes)} (${plan.actions.filter((a) => a.done).length} actions)`)
    } catch (e) { error = e.message }
    busy = ''
  }

  function schedText(sc) {
    if (!sc) return ''
    switch (sc.mode) {
      case 'daily': return `daily at ${sc.at}`
      case 'weekly': return `weekly on ${weekdays[sc.weekday || 0]} at ${sc.at}`
      case 'monthly': return `monthly on the ${ordinal(sc.day || 1)} at ${sc.at}`
      default: return every(Number(sc.everyMin) || 60)
    }
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
    <div><h1>Retention</h1><p class="sub">Policies live on <b>{nodeName(app.node)}</b> and run there on their own schedule, even when this dashboard is offline.</p></div>
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

  <div class="grid cols-2">
    <div class="card tight">
      {#if policies.length === 0}
        <div class="empty">No policies yet. Start from a source on the <a href="#/sources">Sources</a> page or create one.</div>
      {:else}
        <table>
          <thead><tr><th>Policy</th><th>Rules</th><th>Schedule</th><th></th></tr></thead>
          <tbody>
            {#each policies as p, i (p.id)}
              <tr class:sel={editIdx === i && edit}>
                <td>
                  <div class="pname"><Switch checked={p.enabled} disabled={!can('operator')} onchange={() => toggle(i)} size="sm" title={p.enabled ? 'Enabled' : 'Disabled'} /> <b>{p.name}</b></div>
                  <div class="dim small trunc" style="max-width:280px">{describe(p)}</div>
                </td>
                <td class="small muted">{rules(p)}</td>
                <td class="small sched">
                  <div class:dim={!p.enabled}>{schedText(p.schedule).replace(/^./, (c) => c.toUpperCase())}</div>
                  <div class="dim" title={p.lastRun ? `Last scheduled run ${ago(p.lastRun)}` : 'Not run on schedule yet'}>
                    {#if !p.enabled}disabled{:else if !info?.allowWrite}on hold · read-only node{:else if p.nextRun}next {until(p.nextRun)}{/if}
                  </div>
                </td>
                <td class="num">
                  <span class="actions">
                  <button class="sm ghost icon-btn" title="Edit" aria-label="Edit policy" onclick={() => editPolicy(i)}><Icon name="edit" size={15} /></button>
                  <button class="sm ghost icon-btn" title="Preview (dry run)" aria-label="Preview policy" onclick={() => preview(p)}><Icon name="eye" size={15} /></button>
                  {#if can('operator')}
                    <button class="sm ghost icon-btn" title={info?.allowWrite ? 'Run now' : 'Node is read-only'} aria-label="Run policy now" onclick={() => apply([p.id])} disabled={!info?.allowWrite || !!busy}><Icon name="play" size={14} /></button>
                    <button class="sm ghost icon-btn danger" aria-label="Delete policy" title="Delete" onclick={() => remove(i)}><Icon name="trash" size={14} /></button>
                  {/if}
                  </span>
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

        <div class="status" class:on={edit.enabled}>
          <div class="status-text">
            <b>{edit.enabled ? 'Enabled' : 'Disabled'}</b>
            <span class="dim small">
              {#if !edit.enabled}
                Paused. Nothing runs automatically, but you can still run it by hand with <Icon name="play" size={11} /> or <b>Apply all now</b>.
              {:else if info && !info.allowWrite}
                Will run {schedText(edit.schedule)}, but it's on hold while this node is read-only.
              {:else}
                Runs automatically {schedText(edit.schedule)} on {nodeName(app.node)}.
              {/if}
            </span>
          </div>
          <Switch bind:checked={edit.enabled} title={edit.enabled ? 'Disable policy' : 'Enable policy'} />
        </div>

        <h3>Settings</h3>
        <label class="field">Name <input bind:value={edit.name} placeholder="e.g. Keep nginx under 1 GB" /></label>

        <h4>Applies to</h4>
        <div class="chips">
          {#each ['docker', 'file', 'journal'] as k}
            <button type="button" class="chip" class:on={edit.match.kinds.includes(k)} onclick={() => (edit.match.kinds = toggleIn(edit.match.kinds, k))}>{k}</button>
          {/each}
        </div>
        <div class="chips">
          {#each apps as a}
            <button type="button" class="chip" class:on={edit.match.apps.includes(a)} onclick={() => (edit.match.apps = toggleIn(edit.match.apps, a))}>{a}</button>
          {/each}
        </div>
        <label class="field">Specific sources
          <Select block value={null} placeholder="Add a source…" icon="plus" searchable menuWidth={340}
            options={sources.filter((s) => !edit.match.sources.includes(s.id)).map((s) => ({ value: s.id, label: s.name, hint: s.path, group: s.app }))}
            onchange={(v) => (edit.match.sources = [...edit.match.sources, v])} />
        </label>
        {#if edit.match.sources.length}
          <div class="chips">{#each edit.match.sources as s}<button type="button" class="chip on" onclick={() => (edit.match.sources = edit.match.sources.filter((x) => x !== s))}>{srcName[s] || s} <Icon name="x" size={12} /></button>{/each}</div>
        {/if}
        <label class="field">Path glob (optional) <input class="mono" bind:value={edit.match.pathGlob} placeholder="/var/log/nginx/*.log" /></label>
        <p class="dim small" style="margin:0">All conditions must match. Nothing selected = every source on the node.</p>

        <h4>Rules</h4>
        <div class="grid two">
          <label class="field">Max active file size (MB)<input type="number" min="0" bind:value={edit.maxActiveMB} /></label>
          <label class="field">Max total size incl. rotated (MB)<input type="number" min="0" bind:value={edit.maxTotalMB} /></label>
          <label class="field">Keep at most N rotated files<input type="number" min="0" bind:value={edit.maxRotated} /></label>
          <label class="field">Delete rotated older than (days)<input type="number" min="0" bind:value={edit.maxAgeDays} /></label>
        </div>
        <div class="opts">
          <Switch bind:checked={edit.archiveBeforeTruncate} label="Keep a gzip archive before truncating the active file" />
          <Switch bind:checked={edit.compressRotated} label="Gzip uncompressed rotated files (older than 1h)" />
        </div>
        <p class="dim small" style="margin:0">0 = no limit. Active files are truncated in place (same inode), so running apps keep logging. Journald uses <code>journalctl --vacuum-*</code>.</p>

        <h4>Schedule</h4>
        <div class="sched-row">
          <Segmented bind:value={edit.schedule.mode} label="Schedule type" options={modeOpts} />
        </div>
        <div class="sched-row">
          {#if edit.schedule.mode === 'interval'}
            <span class="muted">Every</span>
            <Select bind:value={edit.schedule.everyMin} options={intervalOpts} label="Interval" />
          {:else}
            {#if edit.schedule.mode === 'weekly'}
              <span class="muted">On</span>
              <Select bind:value={edit.schedule.weekday} options={weekdayOpts} label="Weekday" />
            {:else if edit.schedule.mode === 'monthly'}
              <span class="muted">On the</span>
              <Select bind:value={edit.schedule.day} options={dayOpts} label="Day of month" menuWidth={180} />
            {/if}
            <span class="muted">at</span>
            <input type="time" class="mono" bind:value={edit.schedule.at} aria-label="Time of day" />
            {#if info?.timezone}<span class="dim small">node time · {info.timezone}</span>{/if}
          {/if}
        </div>
        <p class="dim small" style="margin:0">
          {#if edit.schedule.mode === 'interval'}
            Counted from when the policy was saved or last ran.
          {:else if edit.schedule.mode === 'monthly' && edit.schedule.day > 28}
            In shorter months it runs on the last day instead.
          {/if}
          {#if !edit.enabled} Applies once the policy is enabled.{/if}
        </p>
        {#if (edit.schedule.mode === 'weekly' || edit.schedule.mode === 'monthly') && (Number(edit.maxActiveMB) || Number(edit.maxTotalMB))}
          <div class="alert warn small">Size limits are only enforced when the policy runs. Logs can grow past {Number(edit.maxActiveMB) ? `${edit.maxActiveMB} MB` : `${edit.maxTotalMB} MB`} for up to a {edit.schedule.mode === 'weekly' ? 'week' : 'month'} between runs.</div>
        {/if}

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
        <button class="ghost sm icon-btn right" aria-label="Close" onclick={() => (plan = null)}><Icon name="x" size={15} /></button>
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
  .editor h3 { margin: 8px 0 0; padding-top: 10px; border-top: 1px solid var(--border); }
  .editor h4 { margin: 6px 0 0; font-size: 12px; font-weight: 620; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); }
  .status {
    display: flex; align-items: center; justify-content: space-between; gap: 14px;
    padding: 12px 14px; border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--panel2);
  }
  .status.on { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: color-mix(in srgb, var(--accent) 7%, var(--panel2)); }
  .status-text { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
  .two { grid-template-columns: 1fr 1fr; gap: 10px; }
  .pname { display: flex; align-items: center; gap: 10px; flex-wrap: nowrap; }
  .opts { display: flex; flex-direction: column; align-items: flex-start; gap: 10px; margin-top: 4px; }
  .sched-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .sched-row input[type='time'] { width: 120px; }
  .sched { white-space: nowrap; }
  .small { font-size: 12px; }
  .ph { padding: 12px 14px; border-bottom: 1px solid var(--border); }
  tr.sel td { background: var(--panel2); }
</style>
