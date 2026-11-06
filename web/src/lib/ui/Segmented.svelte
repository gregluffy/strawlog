<script>
  import Icon from './Icon.svelte'
  // options: [{ value, label, icon?, title? }]
  let { value = $bindable(), options = [], onchange = undefined, size = 'md', block = false, label = undefined } = $props()
  function pick(v) {
    if (v === value) return
    value = v
    onchange?.(v)
  }
  function key(e, i) {
    const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0
    if (!d) return
    e.preventDefault()
    const n = options[(i + d + options.length) % options.length]
    pick(n.value)
    e.currentTarget.parentElement.children[(i + d + options.length) % options.length]?.focus()
  }
</script>

<div class="seg {size}" class:block role="radiogroup" aria-label={label}>
  {#each options as o, i (o.value)}
    <button type="button" role="radio" aria-checked={o.value === value} tabindex={o.value === value ? 0 : -1} class:on={o.value === value} title={o.title} onclick={() => pick(o.value)} onkeydown={(e) => key(e, i)}>
      {#if o.icon}<Icon name={o.icon} size={14} />{/if}{#if o.label}<span>{o.label}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; background: var(--field); border: 1px solid var(--border); border-radius: var(--r-sm); padding: 2px; gap: 2px; height: 34px; }
  .seg.sm { height: 28px; }
  .seg.block { display: flex; width: 100%; }
  .seg button {
    flex: 1; border: 0; background: transparent; color: var(--muted); border-radius: calc(var(--r-sm) - 2px);
    padding: 0 11px; height: 100%; font-size: 13px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; white-space: nowrap;
    transition: background .12s, color .12s; box-shadow: none;
  }
  .seg.sm button { padding: 0 9px; font-size: 12px; }
  .seg button:hover:not(.on) { color: var(--text); background: var(--hover); }
  .seg button.on { background: var(--panel); color: var(--text); font-weight: 600; box-shadow: 0 1px 2px rgba(0, 0, 0, .25), 0 0 0 1px var(--border); }
</style>
