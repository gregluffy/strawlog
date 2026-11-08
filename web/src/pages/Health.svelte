<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { app, go, nodeName } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Segmented from '../lib/ui/Segmented.svelte'

  let findings = $state(null)
  let error = $state('')
  let sev = $state('')
  let open = $state({})

  async function load() {
    findings = null
    try { findings = await api.get(api.n(app.node, '/health')); error = '' } catch (e) { error = e.message; findings = [] }
  }
  onMount(load)
  let lastNode = app.node
  $effect(() => { if (app.node !== lastNode) { lastNode = app.node; load() } })

  let shown = $derived((findings || []).filter((f) => !sev || f.severity === sev))
  // split the title around the source name so it can be highlighted
  function parts(f) {
    const i = f.name ? f.title.indexOf(f.name) : -1
    return i < 0 ? [f.title, '', ''] : [f.title.slice(0, i), f.name, f.title.slice(i + f.name.length)]
  }
  let counts = $derived((findings || []).reduce((a, f) => ((a[f.severity] = (a[f.severity] || 0) + 1), a), {}))
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Health</h1><p class="sub">Problems found on <b>{nodeName(app.node)}</b>: unbounded logs, missing rotation, noisy apps, full disks.</p></div>
    <div class="row">
      <Segmented bind:value={sev} label="Severity" options={[['', 'All'], ['critical', 'Critical'], ['warning', 'Warnings'], ['info', 'Info']].map(([value, l]) => ({ value, label: value && counts[value] ? `${l} · ${counts[value]}` : l }))} />
      <button onclick={load}><Icon name="refresh" size={15} /> Re-check</button>
    </div>
  </div>
  {#if error}<div class="alert err">{error}</div>{/if}
  {#if !findings}
    <div class="empty"><span class="spin"></span></div>
  {:else if shown.length === 0}
    <div class="card empty"><div class="ei" style="color:var(--ok)"><Icon name="ok" size={22} /></div>Nothing to report{sev ? ' at this level' : ''}.</div>
  {:else}
    <div class="list">
      {#each shown as f, i}
        {@const [pre, nm, post] = parts(f)}
        <div class="card f {f.severity}">
          <div class="row">
            <span class="sicon {f.severity}"><Icon name={f.severity === 'info' ? 'info' : 'alert'} size={16} /></span>
            <b class="grow">{pre}{#if nm}<span class="nm">{nm}</span>{/if}{post}</b>
            {#if f.sourceId}
              <button class="sm" onclick={() => go('/logs', { source: f.sourceId })}><Icon name="logs" size={13} /> Logs</button>
              <button class="sm" onclick={() => go('/policies', { source: f.sourceId })}><Icon name="retention" size={13} /> Policy</button>
            {/if}
            {#if f.fix}<button class="sm" onclick={() => (open[i] = !open[i])}>{open[i] ? 'Hide fix' : 'How to fix'}</button>{/if}
          </div>
          {#if f.detail}<p class="muted d">{f.detail}</p>{/if}
          {#if open[i] && f.fix}<pre>{f.fix}</pre>{/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .list { display: flex; flex-direction: column; gap: 8px; }
  .f { padding: 14px 16px; }
  .sicon { width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; flex: none; color: var(--info); background: color-mix(in srgb, var(--info) 14%, transparent); }
  .sicon.critical { color: var(--err); background: color-mix(in srgb, var(--err) 14%, transparent); }
  .sicon.warning { color: var(--warn); background: color-mix(in srgb, var(--warn) 14%, transparent); }
  .nm { color: var(--accent); font-family: var(--mono); font-size: 0.95em; padding: 1px 6px; border-radius: var(--r-xs); background: var(--accent-soft); }
  .d { margin: 6px 0 0 38px; font-size: 13px; }
  .f pre { margin-left: 38px; }
</style>
