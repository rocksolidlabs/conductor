import type { AgentInfo, BroadcastResult, BroadcastSkipReason, CrewCheck, CrewInfo, CrewInput, CrewMember, CrewProbe, CrewManifest, CrewStart, CrewSummary, CrewTask, JoinRunMember, RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { ActivityEntry } from './protocol'
import { joinArgv, splitArgs } from './argv'
import { isEnded } from './attention'
import { EVENT_INFO, feedTime, markOf, type EventColor, type FeedEntry } from './events'

/**
 * The body of POST /api/crews and PUT /api/crews/{id}: the fields the server accepts and nothing else, since it rejects unknown fields. A crew read
 * in full (a CrewInfo from GET /api/crews/{id}, with id, createdAt, updatedAt and manifestRevision) or members carrying page state can be passed as
 * they are. The project manifest goes when it holds something, and the legacy setup only while the crew still has one.
 */
export function toCrewInput(c: Omit<CrewInput, 'tasks' | 'manifest'> & { tasks?: ReadonlyArray<CrewTask | DraftTask>; manifest?: DraftManifest }): CrewInput {
  const out: CrewInput = {
    name: c.name,
    goal: c.goal,
    cwd: c.cwd,
    where: c.where,
    isolation: c.isolation,
    openAfterLaunch: c.openAfterLaunch,
    viewLinkTtlSeconds: c.viewLinkTtlSeconds,
    yolo: c.yolo,
    members: c.members.map(toCrewMember),
  }
  if (c.tasks) out.tasks = plainTasks(c.tasks).map(toCrewTask)
  if (c.setup?.length) out.setup = c.setup.map(toCrewCheck)
  if (c.manifest) {
    const manifest = toCrewManifest(c.manifest)
    if (!manifestEmpty(manifest)) out.manifest = manifest
  }
  return out
}

/** A project manifest as the server accepts it: its fields and nothing else, each list's entries by toCrewCheck and toCrewProbe, empty ones left out. */
export function toCrewManifest(r: CrewManifest): CrewManifest {
  const out: CrewManifest = {}
  if (r.schema) out.schema = r.schema
  if (r.file) out.file = true
  if (r.prerequisites?.length) out.prerequisites = r.prerequisites.map(toCrewCheck)
  if (r.env?.length) out.env = [...r.env]
  if (r.ports?.length) out.ports = [...r.ports]
  if (r.setup?.length) out.setup = r.setup.map(toCrewCheck)
  if (r.services?.length) out.services = r.services.map(toCrewCheck)
  if (r.ready?.length) out.ready = r.ready.map(toCrewProbe)
  if (r.preview) out.preview = r.preview
  if (r.verify?.length) out.verify = r.verify.map(toCrewCheck)
  if (r.cleanup?.length) out.cleanup = r.cleanup.map(toCrewCheck)
  return out
}

/** A readiness probe as the server accepts it: an empty argv, an empty URL and a zero timeout or interval left out. */
export function toCrewProbe(p: CrewProbe): CrewProbe {
  const out: CrewProbe = { id: p.id }
  if (p.argv?.length) out.argv = [...p.argv]
  if (p.url) out.url = p.url
  if (p.timeoutSeconds) out.timeoutSeconds = p.timeoutSeconds
  if (p.intervalSeconds) out.intervalSeconds = p.intervalSeconds
  return out
}

/** Whether a project manifest holds nothing: a crew with an empty manifest has none (the server leaves it out). */
export function manifestEmpty(r: CrewManifest): boolean {
  return !r.schema && !r.file && !r.prerequisites?.length && !r.env?.length && !r.ports?.length && !r.setup?.length && !r.services?.length && !r.ready?.length && !r.preview && !r.verify?.length && !r.cleanup?.length
}

/**
 * A loaded crew as the editor holds it: a legacy setup lifted into its project manifest's setup phase, so the editor has one place for it;
 * a crew with a manifest, or without a setup, as it is. Saved so, the setup is the same manifest to the server, whose count of its changes
 * (manifestRevision) stays where it was.
 */
export function liftSetup<T extends { setup?: CrewCheck[]; manifest?: CrewManifest }>(c: T): T {
  if (!c.setup?.length || (c.manifest && !manifestEmpty(c.manifest))) return c
  const { setup, ...rest } = c
  return { ...rest, manifest: { ...c.manifest, setup: setup.map((ch) => ({ ...ch, argv: [...ch.argv] })) } } as T
}

/** A task as the server accepts it: its fields and nothing else; empty needs and checks left out. */
export function toCrewTask(t: CrewTask): CrewTask {
  const out: CrewTask = { id: t.id, member: t.member, kind: t.kind, instructions: t.instructions }
  if (t.needs?.length) out.needs = [...t.needs]
  if (t.checks?.length) out.checks = t.checks.map(toCrewCheck)
  return out
}

/** A check or setup command as the server accepts it; the text still being typed into its argv (DraftCheck.pending) is left out. */
export function toCrewCheck(c: CrewCheck): CrewCheck {
  const out: CrewCheck = { id: c.id, argv: [...c.argv] }
  if (c.timeoutSeconds) out.timeoutSeconds = c.timeoutSeconds
  return out
}

/** A check in the editor: its fields, and the argv text typed and not yet committed (an unclosed quote), which blocks a save. */
export interface DraftCheck extends CrewCheck {
  pending?: string
}

/**
 * A readiness probe in the editor: its fields, the argv text typed and not yet committed, and whether the row is a command or a URL (`by`),
 * so a row with neither yet keeps the kind it was given. toCrewProbe leaves both out.
 */
export interface DraftProbe extends CrewProbe {
  pending?: string
  by?: 'command' | 'url'
}

/** A project manifest in the editor: its steps and probes as the editor holds them, with what is still being typed. */
export interface DraftManifest extends Omit<CrewManifest, 'prerequisites' | 'setup' | 'services' | 'ready' | 'verify' | 'cleanup'> {
  prerequisites?: DraftCheck[]
  setup?: DraftCheck[]
  services?: DraftCheck[]
  ready?: DraftProbe[]
  verify?: DraftCheck[]
  cleanup?: DraftCheck[]
  /**
   * The lists set aside while the file switch is on (file: true), given back when it is turned off; never sent (toCrewManifest leaves it out).
   * In the draft, not the editor, so that it lasts while the editor is away (the Runs tab) and across a save (keepParked).
   */
  parked?: DraftManifest
}

/** The ids of a project manifest's steps and probes, in every list: what a task's new check is not given (CrewTasksTable). */
export function manifestIds(m: DraftManifest | undefined): string[] {
  const out: string[] = []
  for (const name of ['prerequisites', 'setup', 'services', 'ready', 'verify', 'cleanup'] as const) for (const row of m?.[name] ?? []) out.push(row.id)
  return out
}

/**
 * A number of seconds as typed into a timeout or an interval: nothing (the default) for an empty field, else the number as typed, so a
 * negative or a fraction stays for the rules to mark (checkProblems, manifestProblems) rather than turn into a value nobody typed.
 */
export function typedSeconds(text: string): number | undefined {
  return text.trim() === '' ? undefined : Number(text)
}

/** A typed number of seconds as its field shows it: empty for none. */
export function secondsText(n: number | undefined): string {
  return n === undefined ? '' : String(n)
}

/**
 * A task in the editor: a key that stays while it is renamed or moved, its needs as the keys of the tasks it needs, and its member as the key of
 * the draft member it belongs to, so a rename never touches them; `member` follows that member's name (tasksFollowMembers), through a blank or a
 * duplicate on the way, and stays as the last name when the member is gone (a problem the rules name).
 */
export interface DraftTask extends Omit<CrewTask, 'needs' | 'checks'> {
  key: number
  needKeys: number[]
  memberKey?: number
  checks?: DraftCheck[]
}

let taskKeys = 0

/** The tasks of a saved crew as the editor holds them: keyed, their needs by key. */
export function draftTasks(tasks: readonly CrewTask[], members: ReadonlyArray<Pick<DraftMember, 'name' | 'key'>> = []): DraftTask[] {
  const keyed = tasks.map((t) => ({ t, key: ++taskKeys }))
  const keyOf = new Map(keyed.map(({ t, key }) => [t.id, key]))
  return keyed.map(({ t, key }) => ({
    id: t.id,
    member: t.member,
    memberKey: members.find((m) => m.name === t.member)?.key,
    kind: t.kind,
    instructions: t.instructions,
    key,
    needKeys: (t.needs ?? []).map((n) => keyOf.get(n)).filter((k): k is number => k !== undefined),
    checks: t.checks?.map((c) => ({ ...toCrewCheck(c) })),
  }))
}

/** The editor's tasks as the server takes them: needs by id, pending text left out. Tasks without keys pass as they are. */
export function plainTasks(tasks: ReadonlyArray<CrewTask | DraftTask>): CrewTask[] {
  const idOf = new Map<number, string>()
  for (const t of tasks) if ('key' in t) idOf.set(t.key, t.id)
  return tasks.map((t) => {
    if (!('key' in t)) return t
    const out: CrewTask = { id: t.id, member: t.member, kind: t.kind, instructions: t.instructions }
    const needs = t.needKeys.map((k) => idOf.get(k)).filter((id): id is string => id !== undefined)
    if (needs.length) out.needs = needs
    if (t.checks?.length) out.checks = t.checks.map(toCrewCheck)
    return out
  })
}

/** A member as the server accepts it, in a crew or on its own (POST /api/runs/{run}/members): its fields and nothing else. */
export function toCrewMember(m: CrewMember): CrewMember {
  return {
    name: m.name,
    agentId: m.agentId,
    prompt: m.prompt,
    args: m.args,
    start: { when: m.start.when, member: m.start.member },
  }
}

/** A member's name as the server accepts it (internal/crew memberNamePattern): it becomes a branch and a worktree name. */
export const MEMBER_NAME = /^[a-z0-9][a-z0-9._-]{0,39}$/

/** Why the server would refuse a member name, or '' when it is fine: its pattern, what git refuses in a branch name, and a name another member has. */
export function memberNameError(name: string, taken: readonly string[] = []): string {
  if (!name) return 'Give the member a name'
  if (!MEMBER_NAME.test(name)) return 'Lower-case letters, digits, ".", "_" and "-", starting with a letter or digit, at most 40'
  if (name.includes('..') || name.endsWith('.') || name.endsWith('.lock')) return 'git refuses this as a branch name: no "..", and no "." or ".lock" at the end'
  if (taken.includes(name)) return 'Another member has this name'
  return ''
}

/** Drops what may not end a member name: ".", "-", "_" and ".lock". */
function trimNameEnd(s: string): string {
  let out = s
  for (;;) {
    const next = out.replace(/\.lock$/, '').replace(/[._-]+$/, '')
    if (next === out) return out
    out = next
  }
}

/** A valid member name made from free text (a session's name, an agent id), not one of `taken`: `-2`, `-3`… are added when it is. */
export function memberNameFrom(text: string, taken: readonly string[] = []): string {
  const cleaned = text
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/\.{2,}/g, '.')
    .replace(/^[^a-z0-9]+/, '')
  const base = trimNameEnd(cleaned.slice(0, 40)) || 'agent'
  if (!taken.includes(base)) return base
  for (let i = 2; ; i++) {
    const suffix = `-${i}`
    const name = (trimNameEnd(base.slice(0, 40 - suffix.length)) || 'agent') + suffix
    if (!taken.includes(name)) return name
  }
}

/** A task's or a check's id as the server accepts it (internal/crew taskIDPattern): no dot, since an attempt's branch is `crew/<run>/<task>.<n>`. */
export const TASK_ID = /^[a-z0-9][a-z0-9_-]{0,39}$/

/** The server's bounds on tasks and their project manifest (internal/crew). */
export const TASK_LIMITS = {
  tasks: 128,
  instructions: 8000,
  checks: 8,
  timeout: 3600,
  timeoutDefault: 600,
  argv: 32,
  arg: 4096,
  argvBytes: 8192,
  taskText: 40 << 10,
  /** Prerequisites, setup, ready, verify and cleanup steps of a project manifest, each. */
  manifestSteps: 8,
  /** A prerequisite's and a cleanup step's timeout when none is given (internal/crew steps.go); a setup or verify step's is a check's. */
  prerequisiteTimeoutDefault: 60,
  cleanupTimeoutDefault: 60,
  manifestServices: 4,
  manifestEnv: 32,
  manifestPorts: 8,
  probeTimeout: 3600,
  probeTimeoutDefault: 60,
  probeInterval: 60,
  probeIntervalDefault: 2,
  probeUrl: 2048,
  preview: 512,
  manifestJson: 64 << 10,
} as const

/** Why the server would refuse a task or check id, or '' when it is fine. */
export function taskIdError(id: string, taken: readonly string[] = []): string {
  if (!id) return 'Give it an id'
  if (!TASK_ID.test(id)) return 'Lower-case letters, digits, "_" and "-", starting with a letter or digit, at most 40, no "."'
  if (taken.includes(id)) return 'Another one has this id'
  return ''
}

const bytes = (s: string) => new TextEncoder().encode(s).length

/**
 * How many bytes v takes as JSON as the server writes it (Go's encoding/json, compact, with <, > and & as they are: internal/crew
 * encodeManifest, and encodeTaskJSON, whose indent leaves a string as it is). JSON.stringify's own text is off both ways: the server escapes
 * U+2028 and U+2029, six bytes each where JSON.stringify keeps the three-byte character, and a lone surrogate, which JSON.stringify sends as a
 * six-byte escape, reaches the server as U+FFFD, three bytes. One pass over JSON.stringify's text corrects both without building a string:
 * the tasks table counts every task's text for each row it draws.
 */
function serverJsonBytes(v: unknown): number {
  const text = JSON.stringify(v)
  let n = bytes(text)
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i)
    if (c === 0x2028 || c === 0x2029) n += 3
    else if (c === 0x5c) {
      // An escape, its letter skipped, so the second backslash of an escaped one starts none. A u escape whose code starts with d is a lone
      // surrogate's: JSON.stringify writes a u escape for no other code unit past U+001F.
      if (text.charCodeAt(i + 1) === 0x75 && text.charCodeAt(i + 2) === 0x64) n -= 3
      i++
    }
  }
  return n
}

/** Why the server would refuse a check or setup command (its id, argv and timeout), in order; empty when it is fine. */
export function checkProblems(c: DraftCheck, what: string, taken: readonly string[] = []): string[] {
  const out: string[] = []
  const id = taskIdError(c.id, taken)
  if (id) out.push(`${what}: ${id}`)
  if (!c.argv.length || !c.argv[0]) out.push(`${what}: name a program`)
  if (c.pending) out.push(`${what}: an argument is still being typed: close its quote, or press Enter`)
  if (c.argv.some((a) => a.includes('\0'))) out.push(`${what}: an argument contains NUL`)
  if (c.argv.length > TASK_LIMITS.argv) out.push(`${what}: at most ${TASK_LIMITS.argv} arguments`)
  if (c.argv.some((a) => bytes(a) > TASK_LIMITS.arg)) out.push(`${what}: an argument is longer than ${TASK_LIMITS.arg} bytes`)
  if (c.argv.reduce((n, a) => n + bytes(a), 0) > TASK_LIMITS.argvBytes) out.push(`${what}: the arguments are more than 8 KiB in all`)
  if (c.timeoutSeconds !== undefined && (c.timeoutSeconds < 0 || c.timeoutSeconds > TASK_LIMITS.timeout || !Number.isInteger(c.timeoutSeconds)))
    out.push(`${what}: the timeout is 1 to ${TASK_LIMITS.timeout} seconds`)
  return out
}

/** The variables never passed to a child (internal/pty blocked): a loader that would change what a program runs. */
export const BLOCKED_ENV: readonly string[] = ['LD_PRELOAD', 'LD_LIBRARY_PATH', 'DYLD_INSERT_LIBRARIES']

/** A variable name a project manifest's env may give (internal/crew envNamePattern). */
export const MANIFEST_ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/

/** A project manifest's port name (internal/crew portNamePattern): CONDUCTOR_PORT_<NAME> is its upper case. */
export const MANIFEST_PORT_NAME = /^[a-z][a-z0-9_]{0,15}$/

/** A placeholder: ${CONDUCTOR_PORT_<NAME>}, whatever the name, or ${CONDUCTOR_STATE_DIR} (internal/crew placeholderPattern). */
const PLACEHOLDER = /\$\{CONDUCTOR_(?:PORT_([A-Za-z0-9_]*)|STATE_DIR)\}/g

/** A probe's URL or the preview: http or https on 127.0.0.1, localhost or [::1], the port digits or a port placeholder (internal/crew loopbackURLPattern). */
const LOOPBACK_URL = /^https?:\/\/(?:127\.0\.0\.1|localhost|\[::1\])(?::([0-9]{1,5}|\$\{CONDUCTOR_PORT_[A-Za-z0-9_]*\}))?(?:[/?#][^\x00-\x20\x7f]*)?$/

/** Why the server would refuse an argv as a probe's command: it names no program, holds NUL, or passes the member-args bounds. */
function argvProblems(argv: readonly string[], what: string): string[] {
  const out: string[] = []
  if (!argv.length || !argv[0]) out.push(`${what}: name a program`)
  if (argv.some((a) => a.includes('\0'))) out.push(`${what}: an argument contains NUL`)
  if (argv.length > TASK_LIMITS.argv) out.push(`${what}: at most ${TASK_LIMITS.argv} arguments`)
  if (argv.some((a) => bytes(a) > TASK_LIMITS.arg)) out.push(`${what}: an argument is longer than ${TASK_LIMITS.arg} bytes`)
  if (argv.reduce((n, a) => n + bytes(a), 0) > TASK_LIMITS.argvBytes) out.push(`${what}: the arguments are more than 8 KiB in all`)
  return out
}

/** The port placeholders in s that name no port of the manifest (ports: their upper-case names). */
function strayPlaceholders(s: string, ports: ReadonlySet<string>): string[] {
  return [...s.matchAll(PLACEHOLDER)].filter((m) => m[0] !== '${CONDUCTOR_STATE_DIR}' && !ports.has(m[1] ?? '')).map((m) => m[0])
}

/** Why the server would refuse a probe's URL or the preview, which takes at most `most` bytes. */
function urlProblems(u: string, what: string, most: number, ports: ReadonlySet<string>): string[] {
  if (bytes(u) > most) return [`${what} is longer than ${most} bytes`]
  const m = LOOPBACK_URL.exec(u)
  if (!m) return [`${what} must be http or https on 127.0.0.1, localhost or [::1]`]
  const port = m[1]
  if (port && !port.startsWith('$') && (Number(port) < 1 || Number(port) > 65535)) return [`${what}: the port must be 1 to 65535`]
  return strayPlaceholders(u, ports).map((p) => `${what}: ${p} names no port of the manifest`)
}

/** A whole number from 0 to most: a timeout or an interval left out (0) or within its bound. */
const withinBound = (n: number | undefined, most: number) => n === undefined || (Number.isInteger(n) && n >= 0 && n <= most)

/** What a crew looks like to the project manifest's rules: its manifest, its legacy setup and its tasks (a saved crew's or the editor's). */
export interface ManifestShape {
  manifest?: DraftManifest
  setup?: readonly CrewCheck[]
  tasks?: ReadonlyArray<CrewTask | DraftTask>
}

/** A project manifest's lists of steps and probes, each a list of rows in the editor. */
export type ManifestList = 'prerequisites' | 'setup' | 'services' | 'ready' | 'verify' | 'cleanup'

/** How manifestProblems names a row of each list, `<label> <n>: …` with n from 1, which manifestMarks reads back. */
const MANIFEST_ROWS: Record<ManifestList, string> = {
  prerequisites: 'Prerequisite',
  setup: 'Setup step',
  services: 'Service',
  ready: 'Ready probe',
  verify: 'Verify step',
  cleanup: 'Cleanup step',
}

/**
 * Why the server would refuse a crew's project manifest (internal/crew Manifest.validate, and checkTasks around it), every problem in the server's
 * order; empty when it would be accepted or there is none. A manifest needs tasks, never sits beside a setup, and is held to its bounds: the
 * lists' counts, each step as a check, ids unique across the manifest and none a task's check id, the env and port names, the URLs, and the
 * placeholders (one naming no port, and any in a prerequisite). The server reads conductor.manifest.json itself: with `file`, nothing else is set.
 */
export function manifestProblems(c: ManifestShape): string[] {
  const r = c.manifest
  if (!r || manifestEmpty(r)) return []
  const tasks = plainTasks(c.tasks ?? [])
  if (!tasks.length) return ['A project manifest needs tasks']
  const out: string[] = []
  if (c.setup?.length) out.push("Setup commands beside a project manifest: setup is the manifest's setup phase, put it there")
  if (r.schema) out.push('A project manifest in the crew leaves schema out: it is only set in conductor.manifest.json')
  if (r.file) {
    if (!manifestEmpty({ ...r, file: false })) out.push("With conductor.manifest.json, the manifest is the file at each tree's commit: set nothing else")
    return out
  }
  const env = r.env ?? []
  if (env.length > TASK_LIMITS.manifestEnv) out.push(`At most ${TASK_LIMITS.manifestEnv} env names`)
  for (const [i, name] of env.entries()) {
    if (!MANIFEST_ENV_NAME.test(name)) out.push(`Env ${name || i + 1}: letters, digits and _, not starting with a digit, at most 128`)
    else if (name.startsWith('CONDUCTOR_')) out.push(`Env ${name} is Conductor's own: a CONDUCTOR_ name never comes from the host`)
    else if (BLOCKED_ENV.includes(name)) out.push(`Env ${name} is a loader's variable, never passed on`)
    else if (env.slice(0, i).includes(name)) out.push(`Env ${name} is named twice`)
  }
  const portNames = r.ports ?? []
  if (portNames.length > TASK_LIMITS.manifestPorts) out.push(`At most ${TASK_LIMITS.manifestPorts} ports`)
  const ports = new Set<string>()
  for (const [i, name] of portNames.entries()) {
    if (!MANIFEST_PORT_NAME.test(name)) out.push(`Port ${name || i + 1}: lower-case letters, digits and _, starting with a letter, at most 16`)
    else if (portNames.slice(0, i).includes(name)) out.push(`Port ${name} is named twice`)
    ports.add(name.toUpperCase())
  }
  const checkOf = new Map<string, string>()
  for (const t of tasks) for (const ch of t.checks ?? []) if (!checkOf.has(ch.id)) checkOf.set(ch.id, t.id)
  const ids: string[] = []
  const takeId = (what: string, id: string) => {
    const task = checkOf.get(id)
    if (task !== undefined) out.push(`${what}: ${id} is also a check of task ${task}: a step's id is its own`)
    ids.push(id)
  }
  const steps = (list: readonly DraftCheck[] | undefined, name: ManifestList, words: string, most: number, rule: 'tree' | 'prerequisite' | 'service') => {
    if ((list?.length ?? 0) > most) out.push(`At most ${most} ${words}`)
    for (const [i, ch] of (list ?? []).entries()) {
      const what = `${MANIFEST_ROWS[name]} ${i + 1}`
      if (rule === 'service' && ch.timeoutSeconds) out.push(`${what}: a service has no timeout: it runs as long as its tree`)
      out.push(...checkProblems(ch, what, ids))
      takeId(what, ch.id)
      if (rule === 'prerequisite' && ch.argv.some((a) => a.match(PLACEHOLDER))) out.push(`${what}: a prerequisite runs before any tree: no placeholders`)
      for (const a of ch.argv) for (const p of strayPlaceholders(a, ports)) out.push(`${what}: ${p} names no port of the manifest`)
    }
  }
  steps(r.prerequisites, 'prerequisites', 'prerequisites', TASK_LIMITS.manifestSteps, 'prerequisite')
  steps(r.setup, 'setup', 'setup steps', TASK_LIMITS.manifestSteps, 'tree')
  steps(r.services, 'services', 'services', TASK_LIMITS.manifestServices, 'service')
  const ready = r.ready ?? []
  if (ready.length > TASK_LIMITS.manifestSteps) out.push(`At most ${TASK_LIMITS.manifestSteps} ready probes`)
  for (const [i, p] of ready.entries()) {
    const what = `${MANIFEST_ROWS.ready} ${i + 1}`
    const idErr = taskIdError(p.id, ids)
    if (idErr) out.push(`${what}: ${idErr}`)
    const argv = p.argv ?? []
    if (argv.length && p.url) out.push(`${what}: a probe has a command or a URL, not both`)
    else if (argv.length) {
      out.push(...argvProblems(argv, what))
      for (const a of argv) for (const ph of strayPlaceholders(a, ports)) out.push(`${what}: ${ph} names no port of the manifest`)
    } else if (p.url) out.push(...urlProblems(p.url, `${what}: the URL`, TASK_LIMITS.probeUrl, ports))
    else out.push(`${what}: give it a command or a URL`)
    // The editor's own rule, as checkProblems has it for a step: the text of an argument with its quote still open is not sent.
    if (p.pending) out.push(`${what}: an argument is still being typed: close its quote, or press Enter`)
    if (!withinBound(p.timeoutSeconds, TASK_LIMITS.probeTimeout)) out.push(`${what}: the timeout is 1 to ${TASK_LIMITS.probeTimeout} seconds`)
    if (!withinBound(p.intervalSeconds, TASK_LIMITS.probeInterval)) out.push(`${what}: the interval is 1 to ${TASK_LIMITS.probeInterval} seconds`)
    takeId(what, p.id)
  }
  if (r.preview) out.push(...urlProblems(r.preview, 'The preview', TASK_LIMITS.preview, ports))
  steps(r.verify, 'verify', 'verify steps', TASK_LIMITS.manifestSteps, 'tree')
  steps(r.cleanup, 'cleanup', 'cleanup steps', TASK_LIMITS.manifestSteps, 'tree')
  const size = serverJsonBytes(toCrewManifest(r))
  if (size > TASK_LIMITS.manifestJson) out.push(`The project manifest is ${size} bytes as JSON, more than ${TASK_LIMITS.manifestJson}`)
  return out
}

/** Where the editor marks the problems of a project manifest (manifestMarks). */
export interface ManifestMarks {
  /** Each row's first problem, by its index in its list, with `<label> <n>: ` taken off; '' for a row without one. */
  rows: Record<ManifestList, string[]>
  /** The first problem of the env names, of the ports and of the preview, each in full; '' for none. */
  env: string
  ports: string
  preview: string
  /** Every problem that no row or field holds, in full: the manifest's own (it needs tasks, never sits beside a setup, the file form alone, a list past its count, its size as JSON). */
  manifest: string[]
}

const MANIFEST_ROW = new RegExp(`^(${Object.values(MANIFEST_ROWS).join('|')}) (\\d+): `)

/**
 * The problems of c's project manifest (manifestProblems) where the editor shows them: each under the row or the field it names, the rest at the
 * head of the manifest, so every rule the server holds a manifest to has its words beside what breaks it before a save.
 */
export function manifestMarks(c: ManifestShape): ManifestMarks {
  const r = c.manifest ?? {}
  const rows = Object.fromEntries((Object.keys(MANIFEST_ROWS) as ManifestList[]).map((name) => [name, Array.from({ length: r[name]?.length ?? 0 }, () => '')])) as Record<ManifestList, string[]>
  const marks: ManifestMarks = { rows, env: '', ports: '', preview: '', manifest: [] }
  const listOf = new Map((Object.entries(MANIFEST_ROWS) as Array<[ManifestList, string]>).map(([name, label]) => [label, name]))
  for (const p of manifestProblems(c)) {
    const row = MANIFEST_ROW.exec(p)
    const list = row ? rows[listOf.get(row[1]!)!] : undefined
    const i = row ? Number(row[2]) - 1 : -1
    if (list && i >= 0 && i < list.length) {
      if (!list[i]) list[i] = p.slice(row![0].length)
    } else if (p.startsWith('Env ') || p === `At most ${TASK_LIMITS.manifestEnv} env names`) marks.env ||= p
    else if (p.startsWith('Port ') || p === `At most ${TASK_LIMITS.manifestPorts} ports`) marks.ports ||= p
    else if (p.startsWith('The preview')) marks.preview ||= p
    else marks.manifest.push(p)
  }
  return marks
}

/** What a crew with tasks and its members look like to the task rules: a saved crew's tasks or the editor's, with its project manifest. */
export type TaskShape = Pick<CrewInput, 'setup' | 'isolation'> & {
  goal?: string
  tasks?: ReadonlyArray<CrewTask | DraftTask>
  members: ReadonlyArray<Pick<CrewMember, 'name' | 'start'> & { prompt?: string; key?: number }>
  manifest?: DraftManifest
  /** The legacy setup the editor lifted into the manifest (DraftCrew.liftedSetup). */
  liftedSetup?: readonly CrewCheck[]
}

/**
 * Whether c's project manifest is still the legacy setup the editor lifted into it, unchanged (DraftCrew.liftedSetup). Saved, the crew is that
 * manifest, held to a manifest's rules; launched as it is, it is the crew as stored, whose setup the server holds to a setup's rules alone (an
 * id a task's check shares too, as a legacy setup may). So the manifest's problems hold the save (taskSaveProblems), never that launch.
 */
function liftedAsStored(c: TaskShape): boolean {
  if (!c.liftedSetup?.length || c.setup?.length || !c.manifest) return false
  return JSON.stringify(toCrewManifest(c.manifest)) === JSON.stringify(toCrewManifest({ setup: [...c.liftedSetup] }))
}

/** Characters as the server counts them (runes), not UTF-16 units. */
const chars = (s: string) => [...s].length

/**
 * Why the server would refuse a crew's tasks (internal/crew checkTasks), every problem in the server's order; empty when the crew would be
 * accepted. A crew without tasks has no problem but a task-started member, a setup or a project manifest. The project manifest's come last, as
 * the server checks it last (manifestProblems), unless it is still the setup the editor lifted into it (liftedAsStored).
 */
export function taskProblems(c: TaskShape): string[] {
  const out: string[] = []
  const tasks = plainTasks(c.tasks ?? [])
  const drafts = (c.tasks ?? []).filter((t): t is DraftTask => 'key' in t)
  const manifest = liftedAsStored(c) ? [] : manifestProblems(c)
  if (!tasks.length) {
    if (c.setup?.length) out.push('Setup commands need tasks')
    for (const m of c.members) if (m.start.when === 'task') out.push(`${m.name || 'a member'} starts by its tasks, but the crew has none`)
    out.push(...manifest)
    return out
  }
  if (tasks.length > TASK_LIMITS.tasks) out.push(`At most ${TASK_LIMITS.tasks} tasks`)
  if (c.isolation !== 'worktree') out.push('A crew with tasks gives each member a git worktree: set "Each run" to worktrees')
  for (const m of c.members) if (m.start.when !== 'task') out.push(`${m.name || 'a member'} must start by its tasks in a crew with tasks`)
  if ((c.setup?.length ?? 0) > TASK_LIMITS.checks) out.push(`At most ${TASK_LIMITS.checks} setup commands`)
  const setupIds: string[] = []
  for (const [i, ch] of (c.setup ?? []).entries()) {
    out.push(...checkProblems(ch, `Setup command ${i + 1}`, setupIds))
    setupIds.push(ch.id)
  }
  const names = c.members.map((m) => m.name)
  // A draft task is owned by a member key: one no member has any more (removed, even when another member carries the name now) needs a
  // member chosen, so a save never moves the task to a namesake.
  const keys = new Set(c.members.flatMap((m) => (m.key === undefined ? [] : [m.key])))
  const ids: string[] = []
  const kinds = new Map<string, string>()
  for (const [i, t] of tasks.entries()) {
    const what = t.id ? `Task ${t.id}` : 'A task'
    const idErr = taskIdError(t.id, ids)
    if (idErr) out.push(`${what}: ${idErr}`)
    ids.push(t.id)
    kinds.set(t.id, t.kind)
    if (t.kind !== 'work' && t.kind !== 'review') out.push(`${what}: the kind is work or review`)
    const own = (c.tasks ?? [])[i]
    const orphan = own !== undefined && 'key' in own && own.memberKey !== undefined && keys.size > 0 && !keys.has(own.memberKey)
    if (orphan) out.push(`${what}: its member is gone${t.member ? ` (${t.member})` : ''}: choose one`)
    else if (!names.includes(t.member)) out.push(`${what}: ${t.member ? `${t.member} is not a member` : 'choose its member'}`)
    if (chars(t.instructions) > TASK_LIMITS.instructions) out.push(`${what}: the instructions are at most ${TASK_LIMITS.instructions} characters`)
    if (t.instructions.includes('\0')) out.push(`${what}: the instructions contain NUL`)
    const draft = drafts.find((d) => d.id === t.id && d.key !== undefined)
    const checks = draft?.checks ?? t.checks ?? []
    if (checks.length > TASK_LIMITS.checks) out.push(`${what}: at most ${TASK_LIMITS.checks} checks`)
    if (t.kind === 'review' && checks.length) out.push(`${what}: a review has no checks; its verdict is its result`)
    const checkIds: string[] = []
    for (const [i, ch] of checks.entries()) {
      out.push(...checkProblems(ch, `${what}, check ${i + 1}`, checkIds))
      checkIds.push(ch.id)
    }
  }
  for (const t of tasks) {
    const what = t.id ? `Task ${t.id}` : 'A task'
    for (const n of t.needs ?? []) {
      if (n === t.id) out.push(`${what}: cannot need itself`)
      else if (!ids.includes(n)) out.push(`${what}: needs ${n}, which is not a task`)
    }
    if (t.kind === 'review' && (t.needs?.length !== 1 || kinds.get(t.needs[0]!) !== 'work')) out.push(`${what}: a review needs exactly one work task, the one it reviews`)
  }
  // A depth-first walk: a task still being visited is a cycle, one finished with is not walked again, so a dense graph costs its edges.
  const needs = new Map(tasks.map((t) => [t.id, t.needs ?? []]))
  const state = new Map<string, 'visiting' | 'done'>()
  const walk = (id: string, path: string[]): string => {
    if (state.get(id) === 'done') return ''
    if (state.get(id) === 'visiting') {
      const cycle = [...path.slice(path.indexOf(id)), id]
      return `Task ${cycle[0]}: the needs go round in a cycle: ${cycle.join(' needs ')}`
    }
    state.set(id, 'visiting')
    for (const n of needs.get(id) ?? []) {
      const cycle = walk(n, [...path, id])
      if (cycle) return cycle
    }
    state.set(id, 'done')
    return ''
  }
  for (const t of tasks) {
    const cycle = walk(t.id, [])
    if (cycle) {
      out.push(cycle)
      break
    }
  }
  for (const m of c.members) if (!tasks.some((t) => t.member === m.name)) out.push(`${m.name || 'A member'} has no task; every member of a crew with tasks does one`)
  out.push(...manifest)
  return out
}

/**
 * Why the server would refuse to save a crew's tasks beyond taskProblems (internal/crew ValidateSave): rules a crew is held to as it is
 * saved, not as one saved before is launched, so an unchanged crew from before them still launches. A task needs another once, and its
 * goal, role and instructions as its task.json keeps them (each string as the server writes it as JSON, quotes and escapes: internal/crew
 * taskTextBytes) leave room for the rest of a task file, which never cuts them. A legacy setup the editor lifted into the project manifest,
 * unchanged, is held here to the manifest's rules (liftedAsStored), first, as the server checks a crew before these: saved, it is a manifest;
 * launched as it is, the crew as stored.
 */
export function taskSaveProblems(c: TaskShape): string[] {
  const out: string[] = liftedAsStored(c) ? manifestProblems(c) : []
  for (const t of plainTasks(c.tasks ?? [])) {
    const what = t.id ? `Task ${t.id}` : 'A task'
    const seen = new Set<string>()
    const twice = new Set<string>()
    for (const n of t.needs ?? []) (seen.has(n) ? twice : seen).add(n)
    for (const n of twice) out.push(`${what}: needs ${n} twice`)
    const prompt = c.members.find((m) => m.name === t.member)?.prompt ?? ''
    const text = [c.goal ?? '', prompt, t.instructions].reduce((n, s) => n + serverJsonBytes(s), 0)
    if (text > TASK_LIMITS.taskText) out.push(`${what}: its goal, role and instructions are ${text} bytes as its task file keeps them, more than ${TASK_LIMITS.taskText}`)
  }
  return out
}

/** The problems of one task's row, for the mark under it: those of taskProblems and taskSaveProblems that name it. */
export function taskRowProblems(t: Pick<CrewTask, 'id'>, c: TaskShape): string[] {
  const what = t.id ? `Task ${t.id}` : 'A task'
  return [...taskProblems(c), ...taskSaveProblems(c)].filter((p) => p.startsWith(what + ':') || p.startsWith(what + ','))
}

/** A new task for the editor: done by `member`, its id from the member's name, not one of `taken`. */
export function draftTask(member: string | Pick<DraftMember, 'name' | 'key'>, taken: readonly string[] = []): DraftTask {
  const name = typeof member === 'string' ? member : member.name
  const base = name.replace(/[^a-z0-9_-]+/g, '-').replace(/^[^a-z0-9]+/, '').slice(0, 40) || 'task'
  let id = base
  for (let i = 2; taken.includes(id); i++) id = `${base.slice(0, 40 - `-${i}`.length)}-${i}`
  const out: DraftTask = { id, member: name, kind: 'work', instructions: '', key: ++taskKeys, needKeys: [] }
  if (typeof member !== 'string') out.memberKey = member.key
  return out
}

/** The tasks with each one's member as its draft member is named now, by key: a rename, a blank or a duplicate on the way loses no assignment. A task whose member is gone keeps the last name. */
export function tasksFollowMembers(tasks: readonly DraftTask[], members: ReadonlyArray<Pick<DraftMember, 'name' | 'key'>>): DraftTask[] {
  const nameOf = new Map(members.map((m) => [m.key, m.name]))
  return tasks.map((t) => {
    const name = t.memberKey === undefined ? undefined : nameOf.get(t.memberKey)
    return name === undefined || name === t.member ? t : { ...t, member: name }
  })
}

/** A start condition as one value of the Starts select: `immediately`, `manual` or `after:<member>`. */
export function startValue(s: CrewStart): string {
  return s.when === 'after' ? `after:${s.member ?? ''}` : s.when
}

/** The start condition a value of the Starts select stands for (startValue). */
export function startFrom(value: string): CrewStart {
  if (value.startsWith('after:')) return { when: 'after', member: value.slice('after:'.length) }
  if (value === 'task') return { when: 'task' }
  return { when: value === 'manual' ? 'manual' : 'immediately' }
}

/** What "New crew" starts from: on the server, a worktree per agent, the run view opened after launch, and one member on the first catalog agent. */
export function defaultCrew(catalog: readonly AgentInfo[]): CrewInput {
  const first = catalog[0]
  return {
    name: 'New crew',
    goal: '',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: true,
    members: first ? [{ name: 'lead', agentId: first.id, prompt: '', start: { when: 'immediately' } }] : [],
  }
}

/** A member in the editor: its fields, a key that stays while it is renamed or moved, and its extra arguments as typed. */
export interface DraftMember extends CrewMember {
  key: number
  /** What the Extra args field shows; `args` is it split with splitArgs. */
  argsText: string
}

/**
 * A crew in the editor; no id until it is saved. Its tasks are keyed (DraftTask) and its project manifest holds what is still being typed
 * (DraftManifest). A crew stored with a legacy setup has it lifted into the manifest's setup phase (toDraft), so the editor has one place for it.
 */
export interface DraftCrew extends Omit<CrewInput, 'tasks' | 'manifest'> {
  id?: string
  members: DraftMember[]
  tasks?: DraftTask[]
  manifest?: DraftManifest
  /**
   * The legacy setup lifted into `manifest`, as the crew stores it; never sent. While the manifest is that setup unchanged, the crew launches
   * as it is stored and a manifest's rules hold only its save (taskSaveProblems).
   */
  liftedSetup?: CrewCheck[]
}

let memberKeys = 0

export function draftMember(m: CrewMember): DraftMember {
  return { ...toCrewMember(m), key: ++memberKeys, argsText: joinArgv(m.args ?? []) }
}

/**
 * next, a draft made from the crew as the server has it (toDraft), with the lists prev set aside under the file switch (DraftManifest.parked),
 * while next's manifest is still read from the file: a save or a read of the crew keeps what turning the switch off gives back.
 */
export function keepParked(next: DraftCrew, prev: DraftCrew | undefined): DraftCrew {
  const parked = prev?.manifest?.parked
  if (!parked || !next.manifest?.file) return next
  return { ...next, manifest: { ...next.manifest, parked } }
}

/** A saved crew, or the new one, as the editor holds it: its members and tasks keyed, a legacy setup lifted into its project manifest (liftSetup). */
export function toDraft(c: CrewInput & { id?: string }): DraftCrew {
  const lifted = liftSetup(c)
  const input = toCrewInput(lifted)
  const members = c.members.map(draftMember)
  const out: DraftCrew = { ...input, id: c.id, members, tasks: input.tasks ? draftTasks(input.tasks, members) : undefined }
  // liftSetup hands back the crew itself when it lifts nothing.
  if (lifted !== c) out.liftedSetup = (c.setup ?? []).map(toCrewCheck)
  return out
}

/** The extra arguments typed for a member, split as the Launch dialog splits them; undefined when there are none. */
export function argsFrom(text: string): string[] | undefined {
  const args = splitArgs(text)
  return args.length ? args : undefined
}

/**
 * A crew as the server would store it, for telling a draft from what is saved. A legacy setup counts as the project manifest it lifts into
 * (liftSetup), the one manifest to the server (its revision unchanged), so the editor's lift alone is no change.
 */
export function crewKey(c: Parameters<typeof toCrewInput>[0]): string {
  return JSON.stringify([toCrewInput(liftSetup(c)), pendingTexts(c)])
}

/**
 * The argv text typed into checks, setup commands and the project manifest's steps and probes and not yet committed: part of what is unsaved
 * (a draft with it is kept), never of what is sent.
 */
function pendingTexts(c: Parameters<typeof toCrewInput>[0]): string[] {
  const out: string[] = []
  for (const [i, ch] of (c.setup ?? []).entries()) if ('pending' in ch && ch.pending) out.push(`setup ${i}: ${ch.pending}`)
  for (const t of c.tasks ?? []) {
    if (!('key' in t)) continue
    for (const [i, ch] of (t.checks ?? []).entries()) if (ch.pending) out.push(`${t.key} ${i}: ${ch.pending}`)
  }
  const m: DraftManifest = c.manifest ?? {}
  for (const name of Object.keys(MANIFEST_ROWS) as ManifestList[]) {
    const rows: ReadonlyArray<{ pending?: string }> = m[name] ?? []
    for (const [i, row] of rows.entries()) if (row.pending) out.push(`manifest ${name} ${i}: ${row.pending}`)
  }
  return out
}

/** The page of the Crews list to show after a delete left `left` crews on page `page`: the one before, when the delete emptied a page past the first. */
export function pageAfterDelete(page: number, left: number): number {
  return left === 0 && page > 1 ? page - 1 : page
}

/** The summary GET /api/crews lists for `c`: what the list shows after a save, until the list is read again. */
export function summaryOf(c: CrewInfo): CrewSummary {
  return { id: c.id, name: c.name, goal: c.goal, cwd: c.cwd, where: c.where, isolation: c.isolation, members: c.members.map((m) => ({ name: m.name, agentId: m.agentId, start: m.start })), updatedAt: c.updatedAt }
}

/** A member's state as the run view shows it (memberStatus). */
export type MemberStatus = RunMember['status'] | 'needs_input'

/** The live session of a member: by its id once the run names it, else by the crew tag the session carries. */
function memberSession(run: RunInfo, member: RunMember, sessions: readonly SessionInfo[]): SessionInfo | undefined {
  if (member.sessionId) {
    const s = sessions.find((x) => x.id === member.sessionId)
    if (s) return s
  }
  return sessions.find((x) => x.crew?.runId === run.id && x.crew.member === member.name)
}

/**
 * A member's state from the run as last read, brought up to date by its live session: a member whose session ended `ended`, a starting or
 * running member whose session waits on a prompt `needs_input` (a starting one waits on its trust question: its prompt is held until a
 * person answers), and a pending member whose session exists `starting`. Without a live session the run's own `needsInput` decides.
 */
export function memberStatus(run: RunInfo, member: RunMember, sessions: readonly SessionInfo[]): MemberStatus {
  if (member.status === 'ended') return 'ended'
  const s = memberSession(run, member, sessions)
  if (s && isEnded(s.status)) return 'ended'
  const waiting = s ? s.attention?.state === 'needs_input' : !!member.needsInput
  if (member.status === 'pending') return s ? (waiting ? 'needs_input' : 'starting') : 'pending'
  return waiting ? 'needs_input' : member.status
}

/** How many members wait on a prompt and how many others run (memberStatus). */
export function runCounts(run: RunInfo, sessions: readonly SessionInfo[]): { needs: number; running: number } {
  let needs = 0
  let running = 0
  for (const m of run.members) {
    const st = memberStatus(run, m, sessions)
    if (st === 'needs_input') needs++
    else if (st === 'running') running++
  }
  return { needs, running }
}

/** One line of the crew activity card. */
export interface CrewFeedItem {
  key: string
  at: string
  /** The time of day, as the Events feed shows it. */
  time: string
  /** The member it is about; empty for a line of the run's own log. */
  who: string
  what: string
  color: EventColor
  /** An artifact's URL, shown as text; `link` is set only when linkableUrl allows it. */
  url?: string
  link: string | null
  /** The member's session, for its events. */
  sessionId?: string
}

/**
 * The crew activity card: the Events feed entries of the run's member sessions (`members` maps each session id to its member name) and
 * the run's own log, newest first, at most `limit` lines. Text only, as the Events feed: nothing is rendered as HTML.
 */
export function crewFeed(feed: readonly FeedEntry[], log: readonly ActivityEntry[], members: ReadonlyMap<string, string>, limit = 200): CrewFeedItem[] {
  const items: Array<CrewFeedItem & { ms: number; order: number }> = []
  for (const e of feed) {
    const who = members.get(e.sessionId)
    if (who === undefined) continue
    const label = markOf(e.event, e).label
    items.push({
      key: `e${e.seq}`,
      at: e.at,
      time: e.time,
      who,
      what: e.detail ? `${label}: ${e.detail}` : label,
      color: EVENT_INFO[e.event].color,
      url: e.url,
      link: e.link,
      sessionId: e.sessionId,
      ms: Date.parse(e.at),
      order: e.seq,
    })
  }
  log.forEach((e, i) => {
    items.push({ key: `l${i}`, at: e.at, time: feedTime(e.at), who: '', what: e.message ?? e.type, color: e.type === 'error' ? 'error' : 'neutral', link: null, ms: Date.parse(e.at), order: i })
  })
  items.sort((a, b) => (b.ms || 0) - (a.ms || 0) || b.order - a.order)
  return items.slice(0, limit).map(({ ms: _ms, order: _order, ...item }) => item)
}

const SKIP_REASON: Record<BroadcastSkipReason, string> = {
  needs_input: 'waiting on a prompt',
  not_running: 'not running',
  unknown: 'not a member',
  no_enter: 'typed, no Enter: a question came up',
}

/** Why a member was skipped, in words: a broadcast's reason, or a run chat's `not_sent`. */
export function skipWords(reason: string): string {
  return (SKIP_REASON as Record<string, string>)[reason] ?? reason
}

/** The toast after a broadcast: how many got the line, who, and who was skipped and why. */
export function broadcastSummary(r: BroadcastResult): { title: string; description: string; color: 'success' | 'warning' | 'error' } {
  const total = r.sent.length + r.skipped.length
  const parts: string[] = []
  if (r.sent.length) parts.push(`Sent to ${r.sent.join(', ')}.`)
  if (r.skipped.length) parts.push(`Skipped ${r.skipped.map((s) => `${s.member} (${SKIP_REASON[s.reason] ?? s.reason})`).join(', ')}.`)
  const color = !r.skipped.length ? 'success' : r.sent.length ? 'warning' : 'error'
  return { title: `Sent to ${r.sent.length} of ${total}`, description: parts.join(' '), color }
}

/**
 * The name a broadcast is recorded under: the display name, or else the server's OS user (`whoami`, GET /api/whoami), which the display
 * name defaults to. Undefined, which the server records as `guest`, only when both are empty or the server cannot say.
 */
export async function broadcastByName(displayName: string, whoami: () => Promise<{ user: string }>): Promise<string | undefined> {
  const name = displayName.trim()
  if (name) return name
  try {
    return (await whoami()).user?.trim() || undefined
  } catch {
    return undefined
  }
}

/**
 * The run whose members the sidebar lists alone (its group variant): the run of a run page (`/runs/<id>`), or of the member session a
 * page shows (`/sessions/<id>`), however that page was reached. Undefined everywhere else.
 */
export function sidebarRunFor(path: string, sessions: readonly SessionInfo[]): string | undefined {
  const m = /^\/(runs|sessions)\/([^/]+)\/?$/.exec(path)
  if (!m) return undefined
  let id: string
  try {
    id = decodeURIComponent(m[2]!)
  } catch {
    return undefined
  }
  if (m[1] === 'runs') return id
  return sessions.find((s) => s.id === id)?.crew?.runId
}

/** How long a launch's view link waits for its run page to take it. */
const VIEW_LINK_HOLD_MS = 60_000

let heldViewLink: { runId: string; url: string; ttlSeconds: number; at: number } | null = null
let heldTimer: ReturnType<typeof setTimeout> | undefined

/**
 * Hands the view link a launch returned to the run page the launch opens, in memory only: the run page takes it once (takeViewLink), and
 * a timer drops it once the hold is over, so a token nobody took does not stay in memory.
 */
export function holdViewLink(runId: string, url: string, ttlSeconds: number, now = Date.now()) {
  clearTimeout(heldTimer)
  const held = { runId, url, ttlSeconds, at: now }
  heldViewLink = held
  heldTimer = setTimeout(() => {
    if (heldViewLink === held) heldViewLink = null
  }, VIEW_LINK_HOLD_MS)
}

/** The view link held for this run, once; any call drops what is held, as does a minute nobody took it in. */
export function takeViewLink(runId: string, now = Date.now()): { url: string; ttlSeconds: number } | null {
  const held = heldViewLink
  heldViewLink = null
  // The timer's closure holds the link too: it goes with it.
  clearTimeout(heldTimer)
  heldTimer = undefined
  if (!held || held.runId !== runId || now - held.at > VIEW_LINK_HOLD_MS) return null
  return { url: held.url, ttlSeconds: held.ttlSeconds }
}

/** A run link's member tile: its label and classes, from what its terminal last reported (`heard`), else the run's own status. */
export function joinTileStatus(m: Pick<JoinRunMember, 'status'>, heard?: { status?: string; attention?: string }): { label: string; cls: string; dot: string } {
  const st = heard?.status ?? m.status
  if (heard?.attention === 'needs_input' && (st === 'running' || st === 'starting')) return { label: 'Needs input', cls: 'text-warning', dot: 'bg-warning' }
  if (st === 'running') return { label: 'Running', cls: 'text-success', dot: 'bg-success' }
  return { label: st.replace('_', ' '), cls: 'text-muted', dot: 'bg-neutral-400' }
}

/** A member as the broadcast bar weighs it: whether its session runs, and whether that session waits on a prompt. */
export interface BroadcastMember {
  name: string
  live: boolean
  waiting: boolean
}

/**
 * The run page's broadcast selection. Every member whose session runs is selected unless the person unticked it (`choices`, by name: the
 * person's own ticks and unticks, which outlast every read of the run and every run event); a member that starts later is selected as it
 * appears. `selected` is what the bar sends, in the run's order; `sending` the selected members the server will type into; `waiting` the
 * selected members it will skip because their sessions wait on a prompt.
 */
export function broadcastSelection(members: readonly BroadcastMember[], choices: Readonly<Record<string, boolean>>): { selected: string[]; sending: string[]; waiting: string[] } {
  const selected = members.filter((m) => choices[m.name] ?? m.live)
  return {
    selected: selected.map((m) => m.name),
    sending: selected.filter((m) => m.live && !m.waiting).map((m) => m.name),
    waiting: selected.filter((m) => m.live && m.waiting).map((m) => m.name),
  }
}
