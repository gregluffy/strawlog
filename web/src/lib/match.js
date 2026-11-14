// Client-side mirror of core.matches (internal/core/retention.go), so the policy
// editor can show which sources a policy selects before it is saved.

// globRe compiles a Go filepath.Match pattern (Unix) to a RegExp, or returns
// null when Go would reject it as ErrBadPattern.
export function globRe(pat) {
  let re = '^'
  for (let i = 0; i < pat.length; i++) {
    const c = pat[i]
    if (c === '*') re += '[^/]*'
    else if (c === '?') re += '[^/]'
    else if (c === '\\') {
      if (++i >= pat.length) return null
      re += esc(pat[i])
    } else if (c === '[') {
      let j = i + 1
      let cls = '['
      if (pat[j] === '^') { cls += '^'; j++ }
      let n = 0
      while (j < pat.length && (pat[j] !== ']' || n === 0)) {
        let lo = pat[j]
        if (lo === '-' || lo === ']') return null // Go requires these escaped
        if (lo === '\\') { if (++j >= pat.length) return null; lo = pat[j] }
        j++
        cls += esc(lo)
        if (pat[j] === '-' && pat[j + 1] !== undefined && pat[j + 1] !== ']') {
          let hi = pat[j + 1]
          j += 2
          if (hi === '\\') { if (j >= pat.length) return null; hi = pat[j++] }
          if (hi < lo) return null // an empty range; RegExp would throw
          cls += '-' + esc(hi)
        }
        n++
      }
      if (j >= pat.length || n === 0) return null
      i = j
      re += cls + ']'
    } else re += esc(c)
  }
  try { return new RegExp(re + '$') } catch { return null }
}

const esc = (c) => c.replace(/[\\^$.*+?()[\]{}|/-]/g, '\\$&')

// matches reports whether policy match m selects source s. All set conditions must hold.
export function matches(m, s, re = m.pathGlob ? globRe(m.pathGlob) : undefined) {
  if (m.apps?.length && !m.apps.includes(s.app)) return false
  if (m.kinds?.length && !m.kinds.includes(s.kind)) return false
  if (m.sources?.length && !m.sources.includes(s.id)) return false
  if (m.pathGlob && !(re && re.test(s.path || ''))) return false
  return true
}
