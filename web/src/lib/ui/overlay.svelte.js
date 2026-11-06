// Global dialogs and toasts, rendered by <Overlays /> in App.svelte.
export const overlay = $state({ dialog: null, toasts: [] })

let seq = 0

/** confirm({ title, message, confirmLabel, danger }) → Promise<boolean> */
export function confirmDialog(opts) {
  return new Promise((resolve) => {
    overlay.dialog = { kind: 'confirm', confirmLabel: 'Confirm', ...opts, resolve }
  })
}

/** promptDialog({ title, message, label, value, type, placeholder, confirmLabel, validate }) → Promise<string|null> */
export function promptDialog(opts) {
  return new Promise((resolve) => {
    overlay.dialog = { kind: 'prompt', confirmLabel: 'Save', value: '', type: 'text', ...opts, resolve }
  })
}

/** toast(message, kind = 'ok' | 'err' | 'info', ms) */
export function toast(message, kind = 'ok', ms = 3500) {
  const id = ++seq
  overlay.toasts = [...overlay.toasts, { id, message, kind }]
  setTimeout(() => dismiss(id), kind === 'err' ? Math.max(ms, 6000) : ms)
}

export function dismiss(id) {
  overlay.toasts = overlay.toasts.filter((t) => t.id !== id)
}
