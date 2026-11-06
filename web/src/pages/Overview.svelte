<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, pct, dur } from '../lib/fmt.js'
  import { go, setNode } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'

  let data = $state(null)
  let error = $state('')
  let loading = $state(false)

  async function load() {
    loading = true
    try { data = await api.get('/api/overview'); error = '' } catch (e) { error = e.message }
    loading = false
  }
  onMount(() => { load(); const t = setInterval(load, 60000); return () => clearInterval(t) })

  let totals = $derived.by(() => {
    const t = { logs: 0, growth: 0, crit: 0, warn: 0, online: 0, nodes: 0 }
    for (const n of data || []) {
      t.nodes++
      if (!n.online) continue
      t.online++
      t.logs += n.info?.totalBytes || 0
      t.growth += n.growth24h || 0
      for (const f of n.findings || []) {
        if (f.severity === 'critical') t.crit++
        else if (f.severity === 'warning') t.warn++
      }
    }
    return t
  })

  function open(node, path, params = {}) { setNode(node); go(path, params) }
  function sevCount(n, s) { return (n.findings || []).filter((f) => f.severity === s).length }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Overview</h1><p class="sub">All nodes, what's using disk and what needs attention.</p></div>
    <button onclick={load} disabled={loading}>{#if loading}<span class="spin"></span>{:else}<Icon name="refresh" size={15} />{/if} Refresh</button>
  </div>

  {#if error}<div class="alert err">{error}</div>{/if}

  {#if !data}
    <div class="empty"><span class="spin"></span></div>
  {:else}
    <div class="grid cols-4" style="margin-bottom:14px">
      <div class="card stat"><div class="v">{totals.online}/{totals.nodes}</div><div class="l">nodes online</div></div>
      <div class="card stat"><div class="v">{bytes(totals.logs)}</div><div class="l">total log data</div></div>
      <div class="card stat"><div class="v">+{bytes(totals.growth)}</div><div class="l">growth last 24h</div></div>
      <div class="card stat">
        <div class="v"><span style="color:var(--err)">{totals.crit}</span> <span class="dim">/</span> <span style="color:var(--warn)">{totals.warn}</span></div>
        <div class="l">critical / warnings</div>
      </div>
    </div>

    {#if data.length === 0}
      <div class="card empty">No nodes yet. <a href="#/nodes">Add an agent</a>.</div>
    {/if}

    <div class="grid cols-2">
      {#each data as n (n.id)}
        <div class="card node">
          <div class="row head">
            <span class="dot" class:ok={n.online} class:err={!n.online}></span>
            <h2 style="margin:0">{n.name || n.info?.hostname || n.id}</h2>
            {#if n.local}<span class="badge">this machine</span>{/if}
            {#if n.online && n.info?.allowWrite}<span class="badge ok" title="Retention actions enabled">write</span>{:else if n.online}<span class="badge" title="Read-only node">read-only</span>{/if}
            <span class="right dim small">{n.online ? `${n.latencyMs} ms · up ${dur(n.info.uptimeSec)}` : ''}</span>
          </div>

          {#if !n.online}
            <div class="alert err">{n.error || 'offline'}</div>
          {:else}
            <div class="disks">
              {#each n.info.disks || [] as d}
                <div class="disk">
                  <div class="row small"><span class="mono">{d.path}</span><span class="right muted">{bytes(d.used)} / {bytes(d.total)}</span></div>
                  <div class="bar big"><i style="width:{pct(d.used, d.total)}%; background:{pct(d.used, d.total) > 90 ? 'var(--err)' : pct(d.used, d.total) > 80 ? 'var(--warn)' : 'var(--accent)'}"></i></div>
                </div>
              {/each}
            </div>

            <div class="row stats">
              <div><b>{bytes(n.info.totalBytes)}</b> <span class="muted">logs in {n.info.sourcesCount} sources</span></div>
              <div><b>+{bytes(n.growth24h)}</b> <span class="muted">/ 24h</span></div>
              <button class="ghost sm right" onclick={() => open(n.id, '/health')}>
                {#if sevCount(n, 'critical')}<span class="badge critical">{sevCount(n, 'critical')} critical</span>{/if}
                {#if sevCount(n, 'warning')}<span class="badge warning">{sevCount(n, 'warning')} warnings</span>{/if}
                {#if !sevCount(n, 'critical') && !sevCount(n, 'warning')}<span class="badge ok">healthy</span>{/if}
              </button>
            </div>

            <h3>Biggest apps</h3>
            <div class="apps">
              {#each (n.apps || []).slice(0, 8) as a}
                {@const max = n.apps[0]?.bytes || 1}
                <button class="app ghost" onclick={() => open(n.id, '/sources', { app: a.app })}>
                  <span class="trunc name">{a.app}</span>
                  <span class="bar grow"><i style="width:{pct(a.bytes, max)}%"></i></span>
                  <span class="num">{bytes(a.bytes)}</span>
                  <span class="num dim g">{a.growth24h > 0 ? '+' + bytes(a.growth24h) : ''}</span>
                </button>
              {/each}
            </div>
            {#if n.info.dockerError}<div class="alert warn small" style="margin-top:10px">Docker: {n.info.dockerError}</div>{/if}
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .node { display: flex; flex-direction: column; gap: 14px; }
  .head { gap: 8px; }
  .small { font-size: 12px; }
  .disks { display: flex; flex-direction: column; gap: 8px; }
  .disk .row { margin-bottom: 3px; }
  .stats { gap: 18px; }
  .apps { display: flex; flex-direction: column; gap: 2px; margin-top: -4px; }
  .app { display: grid; grid-template-columns: 140px 1fr 70px 70px; gap: 10px; align-items: center; padding: 4px 6px; text-align: left; }
  .app .num { text-align: right; font-variant-numeric: tabular-nums; font-size: 12.5px; }
  .app .g { font-size: 11.5px; }
  h3 { margin: 0; }
</style>
