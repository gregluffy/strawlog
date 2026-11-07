export function bytes(n) {
  if (n == null || isNaN(n)) return '–'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = Math.abs(n)
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return (n < 0 ? '-' : '') + (i === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)) + ' ' + u[i]
}

export function ago(unix) {
  if (!unix) return '–'
  const s = Math.max(0, Date.now() / 1000 - unix)
  if (s < 60) return 'just now'
  if (s < 3600) return Math.floor(s / 60) + 'm ago'
  if (s < 86400) return Math.floor(s / 3600) + 'h ago'
  return Math.floor(s / 86400) + 'd ago'
}

export function dur(sec) {
  if (sec < 3600) return Math.floor(sec / 60) + 'm'
  if (sec < 86400) return Math.floor(sec / 3600) + 'h'
  return Math.floor(sec / 86400) + 'd'
}

export function shortTs(t) {
  if (!t) return ''
  const d = new Date(t)
  if (isNaN(d)) return t.slice(0, 19)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function pct(a, b) {
  return b ? Math.min(100, (a / b) * 100) : 0
}

export function escapeRe(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// split text into [{t, m}] parts for highlighting
export function highlight(text, query, regex, caseSensitive) {
  if (!query) return [{ t: text, m: false }]
  let re
  try { re = new RegExp(regex ? query : escapeRe(query), caseSensitive ? 'g' : 'gi') } catch { return [{ t: text, m: false }] }
  const out = []
  let last = 0
  let m
  let guard = 0
  while ((m = re.exec(text)) && guard++ < 200) {
    if (m[0] === '') { re.lastIndex++; continue }
    if (m.index > last) out.push({ t: text.slice(last, m.index), m: false })
    out.push({ t: m[0], m: true })
    last = m.index + m[0].length
  }
  if (last < text.length) out.push({ t: text.slice(last), m: false })
  return out
}

export function until(unix) {
  if (!unix) return '–'
  const s = unix - Date.now() / 1000
  if (s < 60) return 'due now'
  if (s < 3600) return 'in ' + Math.ceil(s / 60) + 'm'
  if (s < 86400) {
    const h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60)
    return 'in ' + h + 'h' + (m ? ' ' + m + 'm' : '')
  }
  return 'in ' + Math.round(s / 86400) + 'd'
}

export function every(min) {
  if (!min) return '–'
  if (min % 1440 === 0) return min === 1440 ? 'daily' : `every ${min / 1440}d`
  if (min % 60 === 0) return min === 60 ? 'hourly' : `every ${min / 60}h`
  return `every ${min}m`
}
