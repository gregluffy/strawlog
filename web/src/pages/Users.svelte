<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { ago } from '../lib/fmt.js'
  import { app } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Select from '../lib/ui/Select.svelte'
  import { confirmDialog, promptDialog, toast } from '../lib/ui/overlay.svelte.js'

  let users = $state([])
  let error = $state('')
  let form = $state({ username: '', password: '', role: 'viewer' })
  let busy = $state(false)

  const roles = [
    { id: 'viewer', label: 'Viewer', desc: 'See everything: overview, sources, live logs, search, health, retention previews.' },
    { id: 'operator', label: 'Operator', desc: 'Viewer + create, edit and run retention policies (can delete log data).' },
    { id: 'admin', label: 'Admin', desc: 'Operator + manage nodes, users and notifications.' },
  ]

  async function load() {
    try { users = await api.get('/api/users'); error = '' } catch (e) { error = e.message }
  }
  onMount(load)

  const flash = (t) => toast(t)
  let roleOpts = $derived(roles.map((r) => ({ value: r.id, label: r.label, hint: r.desc })))

  async function create(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await api.post('/api/users', form)
      flash(`User ${form.username} created.`)
      form = { username: '', password: '', role: 'viewer' }
      await load()
    } catch (err) { error = err.message }
    busy = false
  }
  async function patch(u, body, note) {
    error = ''
    try { await api.patch('/api/users/' + u.id, body); flash(note); await load() } catch (e) { error = e.message; await load() }
  }
  async function resetPw(u) {
    const pw = await promptDialog({ title: `Reset password for ${u.username}`, message: 'Their active sessions will be signed out.', label: 'New password (min. 10 characters)', type: 'password', icon: 'key', confirmLabel: 'Reset password', validate: (v) => (v.length < 10 ? 'At least 10 characters' : '') })
    if (pw) patch(u, { password: pw }, `Password of ${u.username} reset; their sessions were ended.`)
  }
  async function remove(u) {
    if (!(await confirmDialog({ title: `Delete ${u.username}?`, message: 'They lose access immediately. This cannot be undone.', confirmLabel: 'Delete user', danger: true }))) return
    error = ''
    try { await api.del('/api/users/' + u.id); flash(`${u.username} deleted.`); await load() } catch (e) { error = e.message }
  }
  function genPw() {
    const a = new Uint8Array(15)
    crypto.getRandomValues(a)
    form.password = btoa(String.fromCharCode(...a)).replace(/[+/=]/g, '').slice(0, 18)
  }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Users</h1><p class="sub">Who can sign in to this dashboard, and what they may do. Roles are enforced by the hub API, not just hidden in the UI.</p></div>
  </div>
  {#if error}<div class="alert err" style="margin-bottom:12px">{error}</div>{/if}

  <div class="card tight" style="margin-bottom:16px; overflow-x:auto">
    <table>
      <thead><tr><th>User</th><th>Role</th><th>Status</th><th>Last login</th><th>Created</th><th></th></tr></thead>
      <tbody>
        {#each users as u (u.id)}
          {@const me = u.id === app.user?.id}
          <tr>
            <td><div class="row" style="gap:10px"><span class="av">{u.username.slice(0, 2).toUpperCase()}</span><b>{u.username}</b>{#if me}<span class="badge accent">you</span>{/if}</div></td>
            <td>
              <Select value={u.role} options={roleOpts} size="sm" disabled={me} menuWidth={300} title={me ? "You can't change your own role" : ''}
                onchange={(v) => patch(u, { role: v }, `${u.username} is now ${v}.`)} />
            </td>
            <td>{#if u.disabled}<span class="badge err">disabled</span>{:else}<span class="badge ok">active</span>{/if}</td>
            <td class="muted">{u.lastLogin ? ago(u.lastLogin) : 'never'}</td>
            <td class="muted">{ago(u.createdAt)}</td>
            <td class="num">
              <span class="actions">
                <button class="sm" onclick={() => resetPw(u)}><Icon name="key" size={13} /> Reset password</button>
                {#if !me}
                  <button class="sm" onclick={() => patch(u, { disabled: !u.disabled }, `${u.username} ${u.disabled ? 'enabled' : 'disabled'}.`)}>{u.disabled ? 'Enable' : 'Disable'}</button>
                  <button class="sm ghost icon-btn danger" title="Delete user" aria-label="Delete user" onclick={() => remove(u)}><Icon name="trash" size={14} /></button>
                {/if}
              </span>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="addu">
    <form class="card form" onsubmit={create}>
      <h2>Add user</h2>
      <label class="field">Username <input bind:value={form.username} autocomplete="off" pattern="[a-zA-Z0-9._\-]{'{2,32}'}" required /></label>
      <label class="field">Initial password (min. 10)
        <div class="row"><input class="grow mono" bind:value={form.password} autocomplete="new-password" minlength="10" required /><button type="button" onclick={genPw}><Icon name="key" size={14} /> Generate</button></div>
      </label>
      <div class="field">Role
        <div class="roles">
          {#each roles as r}
            <label class="role" class:on={form.role === r.id}>
              <input type="radio" name="role" value={r.id} bind:group={form.role} />
              <div><b>{r.label}</b><div class="muted small">{r.desc}</div></div>
            </label>
          {/each}
        </div>
      </div>
      <div><button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if} Create user</button></div>
      <p class="dim small" style="margin:0">Share the password through a safe channel. The user can change it under Settings.</p>
    </form>
  </div>
</div>

<style>
  .addu { max-width: 720px; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .small { font-size: 12px; }
  .av { width: 28px; height: 28px; border-radius: 50%; display: grid; place-items: center; background: var(--hover); color: var(--muted); font-size: 11px; font-weight: 700; }
  .roles { display: flex; flex-direction: column; gap: 6px; color: var(--text); }
  .role { display: flex; gap: 10px; align-items: flex-start; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--r-md); transition: border-color .12s, background .12s; cursor: pointer; font-size: 13px; }
  .role.on { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .role input { margin-top: 3px; }
</style>
