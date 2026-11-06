<script>
  import { tick } from 'svelte'
  import Icon from './Icon.svelte'
  import { overlay, dismiss } from './overlay.svelte.js'

  let input = $state('')
  let error = $state('')
  let box = $state()
  let field = $state()

  $effect(() => {
    const d = overlay.dialog
    if (d) {
      input = d.value || ''
      error = ''
      tick().then(() => (field || box?.querySelector('button.primary, button.danger.solid'))?.focus())
    }
  })

  function close(result) {
    const d = overlay.dialog
    overlay.dialog = null
    d?.resolve(result)
  }
  function ok(e) {
    e?.preventDefault()
    const d = overlay.dialog
    if (d.kind === 'prompt') {
      const err = d.validate?.(input)
      if (err) { error = err; return }
      close(input)
    } else close(true)
  }
  function key(e) {
    if (e.key === 'Escape') { e.preventDefault(); close(overlay.dialog.kind === 'prompt' ? null : false) }
    if (e.key === 'Tab') { // keep focus inside the dialog
      const f = [...box.querySelectorAll('button, input')].filter((x) => !x.disabled)
      if (!f.length) return
      if (e.shiftKey && document.activeElement === f[0]) { e.preventDefault(); f.at(-1).focus() }
      else if (!e.shiftKey && document.activeElement === f.at(-1)) { e.preventDefault(); f[0].focus() }
    }
  }
  const icons = { ok: 'ok', err: 'alert', info: 'info' }
</script>

{#if overlay.dialog}
  {@const d = overlay.dialog}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="backdrop" onmousedown={(e) => e.target === e.currentTarget && close(d.kind === 'prompt' ? null : false)}>
    <div class="dialog" bind:this={box} role="dialog" aria-modal="true" aria-labelledby="dlg-title" tabindex="-1" onkeydown={key}>
    <form class="dform" onsubmit={ok}>
      <div class="head">
        {#if d.danger}<span class="dicon danger"><Icon name="alert" size={18} /></span>{:else if d.kind === 'prompt'}<span class="dicon"><Icon name={d.icon || 'edit'} size={18} /></span>{:else}<span class="dicon"><Icon name="info" size={18} /></span>{/if}
        <div>
          <h2 id="dlg-title">{d.title}</h2>
          {#if d.message}<p class="muted msg">{d.message}</p>{/if}
        </div>
      </div>
      {#if d.kind === 'prompt'}
        <label class="field">{d.label || ''}
          <input bind:this={field} bind:value={input} type={d.type} placeholder={d.placeholder || ''} autocomplete="off" class:mono={d.mono} />
        </label>
        {#if error}<div class="ferr">{error}</div>{/if}
      {/if}
      <div class="actions">
        <button type="button" class="ghost" onclick={() => close(d.kind === 'prompt' ? null : false)}>Cancel</button>
        <button type="submit" class={d.danger ? 'danger solid' : 'primary'}>{d.confirmLabel}</button>
      </div>
    </form>
    </div>
  </div>
{/if}

<div class="toasts" aria-live="polite">
  {#each overlay.toasts as t (t.id)}
    <div class="toast {t.kind}">
      <Icon name={icons[t.kind] || 'info'} size={17} />
      <span class="grow">{t.message}</span>
      <button class="ghost x" aria-label="Dismiss" onclick={() => dismiss(t.id)}><Icon name="x" size={14} /></button>
    </div>
  {/each}
</div>

<style>
  .backdrop { position: fixed; inset: 0; z-index: 2000; background: rgba(5, 7, 10, .55); backdrop-filter: blur(3px); display: grid; place-items: center; padding: 16px; animation: fade .12s; }
  @keyframes fade { from { opacity: 0; } }
  .dialog { width: 100%; max-width: 440px; background: var(--panel); border: 1px solid var(--border-strong); border-radius: var(--r-lg); box-shadow: var(--shadow-lg); padding: 20px; display: flex; flex-direction: column; gap: 14px; animation: rise .16s ease-out; }
  @keyframes rise { from { opacity: 0; transform: translateY(8px) scale(.98); } }
  .dform { display: flex; flex-direction: column; gap: 14px; }
  .dialog:focus { outline: none; }
  .head { display: flex; gap: 12px; align-items: flex-start; }
  .head h2 { margin: 2px 0 0; font-size: 16px; }
  .msg { margin: 6px 0 0; font-size: 13px; line-height: 1.5; }
  .dicon { width: 34px; height: 34px; border-radius: 10px; display: grid; place-items: center; flex: none; background: var(--accent-soft); color: var(--accent); }
  .dicon.danger { background: color-mix(in srgb, var(--err) 15%, transparent); color: var(--err); }
  .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
  .ferr { color: var(--err); font-size: 12.5px; margin-top: -6px; }

  .toasts { position: fixed; right: 16px; bottom: 16px; z-index: 2100; display: flex; flex-direction: column; gap: 8px; width: min(380px, calc(100vw - 32px)); }
  .toast { display: flex; align-items: flex-start; gap: 10px; padding: 11px 12px; background: var(--popover); border: 1px solid var(--border-strong); border-left: 3px solid var(--info); border-radius: var(--r-md); box-shadow: var(--shadow-lg); font-size: 13px; animation: slide .18s ease-out; }
  .toast.ok { border-left-color: var(--ok); } .toast.ok :global(.icon) { color: var(--ok); }
  .toast.err { border-left-color: var(--err); } .toast.err :global(.icon) { color: var(--err); }
  .toast.info :global(.icon) { color: var(--info); }
  .toast :global(.icon) { margin-top: 1px; }
  .toast .x { padding: 2px; height: auto; color: var(--dim); }
  .toast .x :global(.icon) { color: var(--dim); }
  @keyframes slide { from { opacity: 0; transform: translateY(10px); } }
</style>
