<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { app, go, nodeName } from '../lib/state.svelte.js'

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
  let counts = $derived((findings || []).reduce((a, f) => ((a[f.severity] = (a[f.severity] || 0) + 1), a), {}))
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Health</h1><p class="sub">Problems found on <b>{nodeName(app.node)}</b>: unbounded logs, missing rotation, noisy apps, full disks.</p></div>
    <div class="row">
      {#each [['', 'All'], ['critical', 'Critical'], ['warning', 'Warnings'], ['info', 'Info']] as [k, label]}
        <button class:primary={sev === k} onclick={() => (sev = k)}>{label}{k && counts[k] ? ` (${counts[k]})` : ''}</button>
      {/each}
      <button onclick={load}>Re-check</button>
    </div>
  </div>
  {#if error}<div class="alert err">{error}</div>{/if}
  {#if !findings}
    <div class="empty"><span class="spin"></span></div>
  {:else if shown.length === 0}
    <div class="card empty">✓ Nothing to report{sev ? ' at this level' : ''}.</div>
  {:else}
    <div class="list">
      {#each shown as f, i}
        <div class="card f {f.severity}">
          <div class="row">
            <span class="badge {f.severity}">{f.severity}</span>
            <b class="grow">{f.title}</b>
            {#if f.sourceId}
              <button class="sm" onclick={() => go('/logs', { source: f.sourceId })}>View logs</button>
              <button class="sm" onclick={() => go('/policies', { source: f.sourceId })}>Add policy</button>
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
  .f { padding: 12px 14px; border-left: 3px solid var(--info); }
  .f.critical { border-left-color: var(--err); }
  .f.warning { border-left-color: var(--warn); }
  .d { margin: 6px 0 0; font-size: 13px; }
</style>
