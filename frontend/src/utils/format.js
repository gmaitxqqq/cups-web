export function formatFileSize(bytes) {
  if (!bytes) return '0 B'
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

export function formatTime(iso) {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
  } catch { return iso }
}

export function formatPrinterName(uri) {
  if (!uri) return ''
  const parts = uri.split('/')
  return parts[parts.length - 1] || uri
}

export function formatDurationSeconds(totalSeconds) {
  if (!totalSeconds || totalSeconds < 0) return '未知'
  const d = Math.floor(totalSeconds / 86400)
  const h = Math.floor((totalSeconds % 86400) / 3600)
  const m = Math.floor((totalSeconds % 3600) / 60)
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分钟`
  if (m > 0) return `${m}分钟`
  return `${totalSeconds}秒`
}

// formatStateDuration 计算从 ISO 时间字符串到现在经过了多久
export function formatStateDuration(isoStr) {
  if (!isoStr) return '未知'
  const past = new Date(isoStr)
  if (isNaN(past.getTime())) return '未知'
  const diffMs = Date.now() - past.getTime()
  if (diffMs < 0) return '未知'
  const totalSeconds = Math.floor(diffMs / 1000)
  const d = Math.floor(totalSeconds / 86400)
  const h = Math.floor((totalSeconds % 86400) / 3600)
  const m = Math.floor((totalSeconds % 3600) / 60)
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分钟`
  if (m > 0) return `${m}分钟`
  return `${totalSeconds}秒`
}

export function statusColor(status) {
  const map = { queued: 'info', printed: 'success', failed: 'error', cancelled: 'neutral' }
  return map[status] || 'neutral'
}

export function statusText(status) {
  const map = { queued: '排队中', printed: '已打印', failed: '失败', cancelled: '已取消' }
  return map[status] || status
}

export function printerStateColor(state) {
  const map = { idle: 'success', processing: 'warning', stopped: 'error' }
  return map[state] || 'neutral'
}

export function printerStateText(state) {
  const map = { idle: '空闲', processing: '打印中', stopped: '已停止' }
  return map[state] || state || '未知'
}

export function markerLevelColor(level) {
  if (level === undefined || level === null) return 'text-muted'
  if (level <= 10) return 'text-error font-bold'
  if (level <= 25) return 'text-warning font-medium'
  return 'text-success'
}

export function markerBarColor(level) {
  if (level === undefined || level === null) return 'bg-muted'
  if (level <= 10) return 'bg-error'
  if (level <= 25) return 'bg-warning'
  return 'bg-success'
}
