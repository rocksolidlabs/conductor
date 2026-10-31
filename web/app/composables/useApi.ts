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

/** $fetch wrapper that attaches the workbench token and normalises errors. */
export function useApi() {
  const { httpBase } = useApiBase()
  const admin = useWorkbenchToken()

  /** The error a failed request throws: the server's code and message, or `network` when it could not be reached. */
  function normalise(e: unknown, explicitToken: boolean): ApiError {
    const fe = e as FetchError<unknown>
    const status = fe.statusCode ?? 0
    const data = errorBody(fe.data)
    const code = data?.error?.code ?? (status === 0 ? 'network' : 'http_error')
    const message = data?.error?.message ?? (status === 0 ? 'Cannot reach the server' : fe.message)
    if (status === 401 && !explicitToken) admin.needsToken.value = true
    return new ApiError(status, code, message, data?.error ?? {}, data ?? fe.data)
  }

  async function request<T>(path: string, opts: { method?: string; body?: unknown; token?: string; query?: Record<string, string>; base?: string } = {}): Promise<T> {
    const token = opts.token ?? admin.token.value
    try {
      return await $fetch<T>((opts.base ?? httpBase.value) + path, {
        method: (opts.method ?? 'GET') as 'GET',
        body: opts.body as Record<string, unknown> | undefined,
        query: opts.query,
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
    } catch (e) {
      throw normalise(e, opts.token !== undefined)
    }
  }

  /**
   * A GET whose body is text, with the reply's headers: `request` returns the body alone. The body is read as bytes and decoded
   * for `text`, the bytes kept as they came (a log saved is the log the server hashed). The bearer header goes with it, so a log is
   * fetched, never a link the browser follows (which would carry no token); errors are normalised as `request`'s are.
   */
  async function requestText(path: string, opts: { token?: string; base?: string } = {}): Promise<TextReply> {
    const token = opts.token ?? admin.token.value
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
      throw normalise(e, opts.token !== undefined)
    }
  }

  return { request, requestText }
}
