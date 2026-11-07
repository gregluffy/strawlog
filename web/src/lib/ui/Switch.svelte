<script>
  let { checked = $bindable(), onchange = undefined, disabled = false, label = '', size = 'md', title = undefined } = $props()
  function toggle() {
    if (disabled) return
    checked = !checked
    onchange?.(checked)
  }
</script>

<button type="button" class="sw {size}" class:on={!!checked} role="switch" aria-checked={!!checked} {disabled} {title} onclick={toggle}>
  <span class="track"><span class="knob"></span></span>
  {#if label}<span class="lbl">{label}</span>{/if}
</button>

<style>
  .sw { display: inline-flex; align-items: center; justify-content: flex-start; gap: 8px; background: none; border: 0; padding: 0; height: auto; min-height: 20px; color: var(--text); font: inherit; text-align: left; white-space: normal; cursor: pointer; box-shadow: none; align-self: flex-start; }
  .sw:hover:not(:disabled) { background: none; }
  .sw:disabled { opacity: .5; cursor: default; }
  .track { width: 34px; height: 20px; border-radius: 99px; background: var(--border-strong); position: relative; transition: background .15s; flex: none; }
  .knob { position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 3px rgba(0, 0, 0, .35); transition: transform .15s; }
  .on .track { background: var(--accent); }
  .on .knob { transform: translateX(14px); }
  .sm .track { width: 28px; height: 16px; }
  .sm .knob { width: 12px; height: 12px; }
  .sm.on .knob { transform: translateX(12px); }
  .lbl { font-size: 13px; }
  .sw:focus-visible .track { outline: 2px solid var(--ring-strong); outline-offset: 2px; }
</style>
