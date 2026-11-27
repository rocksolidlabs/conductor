import { resumeDestination, resumeFailure } from '~/utils/history'

/** How a resume from a record ended for the place that asked: started (its page opened), sent on to the record's page, or refused there. */
export type RecordResumeEnd = 'started' | 'record' | 'refused'

/**
 * A resume from a session's record as the palette and the welcome ask it, once the person said yes (History's rule: a resume asks
 * first). Started: "<name> resumed" and the new session's page. An outcome not known or still pending, and a refusal only the record's
 * page asks about in full (the launch's variables, a lost record's second yes): said, and the record's page opened, which asks there.
 * Any other refusal: "<name> did not resume" with the route's words, and nothing opened. Never sends withoutEnv or confirmLost.
 */
export function useRecordResume(o: { go: (to: string) => void }) {
  const history = useHistory()
  const toast = useToast()

  async function resume(id: string, label: string): Promise<RecordResumeEnd> {
    const record = `/history/sessions/${encodeURIComponent(id)}`
    try {
      const r = await history.resumeRecord(id)
      const to = resumeDestination(r)
      toast.add({ title: `${label} resumed`, description: r.notice, icon: 'i-lucide-history', color: 'success' })
      if (to) o.go(to.to)
      return 'started'
    } catch (e) {
      // An unknown or pending outcome is said as such, never as "did not resume", and its record opens: its receipts say how it ended.
      // A question only the record's page asks in full (the launch's variables, a lost record's second yes): that page, saying why.
      const f = resumeFailure(e, { unknown: 'its record, opened now, keeps the receipt that says whether it did', pending: 'Its record, opened now, keeps the receipt that says how it ends.' })
      const title = f.outcome === 'refused' || f.outcome === 'failed' ? `${label} did not resume` : `${label}: ${f.title.charAt(0).toLowerCase()}${f.title.slice(1)}`
      toast.add({ title, description: f.description, icon: f.icon, color: f.color })
      if (f.outcome === 'unknown' || f.outcome === 'pending' || f.code === 'env_not_kept' || f.code === 'lost_unconfirmed') {
        o.go(record)
        return 'record'
      }
      return 'refused'
    }
  }

  return { resume }
}
