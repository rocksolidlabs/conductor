// A time as the app's words say it beside a moment it is read against: the time alone on that moment's local day, the day and month
// with it on any other ("16 Nov 14:02"), so a time days old never reads as today's. In the viewer's zone, 24-hour. Pure, no import.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** A time in words against `now` (ms): "14:02" on now's local day, "16 Nov 14:02" another day; '' for none or a time not read. */
export function atWords(iso: string | undefined, now: number): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const hm = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  return new Date(now).toDateString() === t.toDateString() ? hm : `${t.getDate()} ${MONTHS[t.getMonth()]} ${hm}`
}
