import type { FetchError } from 'ofetch'

export interface ApiErrorBody {
  error?: { code: string; message: string; [field: string]: unknown }
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    /** The error object as the server sent it, with the fields its route adds (docs/protocol.md). */
    public details: Record<string, unknown> = {},
    /** The whole reply, for a route that answers more beside `error` (a command's `receipt`). */
    public body: unknown = undefined,
  ) {
    super(message)
  }
}

/** Base URL helpers shared by HTTP and WebSocket clients. */
export function useApiBase() {
  const config = useRuntimeConfig()
  const httpBase = computed(() => (config.public.apiBase as string) || '')
  const wsBase = computed(() => {
    const base = httpBase.value
    if (base) return base.replace(/^http/, 'ws')
    if (import.meta.client) {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      return `${proto}//${location.host}`
    }
    return ''
  })
  return { httpBase, wsBase }
}

/**
 * A body read as text, with its bytes as the server sent them and the reply's headers: a step's log and its
 * `X-Conductor-Log-SHA256`, which is the hash of `bytes` (a log can hold bytes that are no UTF-8, such as a character its bound
 * cut, which `text` shows as U+FFFD).
 */
export interface TextReply {
  text: string
  bytes: Uint8Array
  headers: Headers
}

/**
 * A failed reply's body as the server sent it: an object for a JSON request, and for a text one (read as bytes) the JSON text,
 * which ofetch hands back unparsed, read here as the same object; anything that is no object is no error body.
 */
function errorBody(data: unknown): ApiErrorBody | undefined {
  if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) data = new TextDecoder().decode(data)
  if (typeof data === 'string') {
    try {
      data = JSON.parse(data)
    } catch {
      return undefined
    }
  }
  return data !== null && typeof data === 'object' ? (data as ApiErrorBody) : undefined
}

/**
 * The reads waiting for the page to connect to go, by what they ask (a GET with no signal of its own): a store that asks the same
 * again while it waits (a poll's tick) shares the one read, which goes once when a token is taken.
 */
const waiting = new Map<string, Promise<unknown>>()

/** $fetch wrapper that attaches the workbench token and normalises errors. */
export function useApi() {
  const { httpBase } = useApiBase()
  const admin = useWorkbenchToken()

  /**
   * The token a request goes with: the caller's own (`explicit`, a link's or none, never held back), else the workbench token held
   * once the page to connect is gone. While that page shows, nothing goes with the missing or refused token: a read waits, and goes
   * once a token the server took is held, and no store sees an error it would retry on a timer. A request that changes something
   * (`write`) is never sent later under another token: once the page goes it is answered as not sent, as a send with no connection
   * is. An abort, before or while it waits, throws as an aborted fetch does; so does a read whose page went with no token taken.
   */
  async function admitted(explicit: string | undefined, signal?: AbortSignal, write = false): Promise<string> {
    if (explicit !== undefined) return explicit
    if (admin.gated.value) {
      let taken: boolean
      try {
        taken = await admin.connected(signal)
      } catch {
        throw new ApiError(0, 'network', 'Cannot reach the server')
      }
      if (write) throw new ApiError(0, 'network', 'Not sent: the app was not connected to the server')
      if (!taken) throw new ApiError(0, 'network', 'Cannot reach the server')
    }
    return admin.token.value
  }

  /**
   * The error a failed request throws: the server's code and message, or `network` when it could not be reached. A 401 is told to the
   * workbench token's store only when the request carried that token, as `sent` (admin.answered judges it by that token, not by the
   * one held when the reply lands); a token of the caller's own (`explicitToken`) is told nothing. A success here accepts no token:
   * routes beside the workbench's admit other credentials too; the events stream itself answering, which the server does for an
   * operator's credential alone, does (openEvents).
   */
  function normalise(e: unknown, explicitToken: boolean, sent: string): ApiError {
    const fe = e as FetchError<unknown>
    const status = fe.statusCode ?? 0
    const data = errorBody(fe.data)
    const code = data?.error?.code ?? (status === 0 ? 'network' : 'http_error')
    const message = data?.error?.message ?? (status === 0 ? 'Cannot reach the server' : fe.message)
    if (status === 401 && !explicitToken) admin.answered(sent, false)
    return new ApiError(status, code, message, data?.error ?? {}, data ?? fe.data)
  }

  /** `signal` aborts the request (a search overtaken by the next key): it then throws as a network error does, which its caller drops. */
  async function request<T>(path: string, opts: { method?: string; body?: unknown; token?: string; query?: Record<string, string>; base?: string; signal?: AbortSignal } = {}): Promise<T> {
    const write = !['GET', 'HEAD'].includes((opts.method ?? 'GET').toUpperCase())
    if (opts.token === undefined && !opts.signal && !write && admin.gated.value) {
      const key = `${opts.base ?? httpBase.value}${path} ${JSON.stringify(opts.query ?? {})}`
      let read = waiting.get(key) as Promise<T> | undefined
      if (read) admin.waited.value++
      if (!read) {
        const entry: { read?: Promise<T> } = {}
        entry.read = (async () => {
          try {
            await admitted(undefined)
          } finally {
            // Gone (its wait ended, with a token taken or not): a later read of the key is a read of its own.
            if (waiting.get(key) === entry.read) waiting.delete(key)
          }
          return send<T>(path, opts, admin.token.value)
        })()
        read = entry.read
        waiting.set(key, read)
      }
      return read
    }
    // Sent at once when nothing holds it back, in the caller's own tick.
    return send<T>(path, opts, opts.token !== undefined || !admin.gated.value ? (opts.token ?? admin.token.value) : await admitted(opts.token, opts.signal, write))
  }

  async function send<T>(path: string, opts: { method?: string; body?: unknown; token?: string; query?: Record<string, string>; base?: string; signal?: AbortSignal }, token: string): Promise<T> {
    try {
      return await $fetch<T>((opts.base ?? httpBase.value) + path, {
        method: (opts.method ?? 'GET') as 'GET',
        body: opts.body as Record<string, unknown> | undefined,
        query: opts.query,
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        ...(opts.signal ? { signal: opts.signal } : {}),
      })
    } catch (e) {
      throw normalise(e, opts.token !== undefined, token)
    }
  }

  /**
   * A GET whose body is text, with the reply's headers: `request` returns the body alone. The body is read as bytes and decoded
   * for `text`, the bytes kept as they came (a log saved is the log the server hashed). The bearer header goes with it, so a log is
   * fetched, never a link the browser follows (which would carry no token); errors are normalised as `request`'s are.
   */
  async function requestText(path: string, opts: { token?: string; base?: string } = {}): Promise<TextReply> {
    const token = opts.token !== undefined || !admin.gated.value ? (opts.token ?? admin.token.value) : await admitted(opts.token)
    try {
      const res = await $fetch.raw<ArrayBuffer>((opts.base ?? httpBase.value) + path, {
        method: 'GET',
        responseType: 'arrayBuffer',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
      const data = res._data as ArrayBuffer | undefined
      const bytes = data ? new Uint8Array(data) : new Uint8Array()
      return { text: new TextDecoder().decode(bytes), bytes, headers: res.headers }
    } catch (e) {
      throw normalise(e, opts.token !== undefined, token)
    }
  }

  /**
   * A GET whose body is a file, as a Blob of the type the server gave: a Dispatch attachment, fetched with the bearer header and shown
   * through an object URL, since an `img` or a link the browser follows would carry no token. Errors are normalised as `request`'s are.
   */
  async function requestBlob(path: string, opts: { token?: string; base?: string } = {}): Promise<Blob> {
    const token = opts.token !== undefined || !admin.gated.value ? (opts.token ?? admin.token.value) : await admitted(opts.token)
    try {
      return await $fetch<Blob>((opts.base ?? httpBase.value) + path, {
        method: 'GET',
        responseType: 'blob',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
    } catch (e) {
      throw normalise(e, opts.token !== undefined, token)
    }
  }

  return { request, requestText, requestBlob }
}
