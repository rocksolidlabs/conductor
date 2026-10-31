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

/** A body read as text, with the reply's headers: a step's log and its `X-Conductor-Log-SHA256`. */
export interface TextReply {
  text: string
  headers: Headers
}

/**
 * A failed reply's body as the server sent it: an object for a JSON request, and for a text one (`responseType: 'text'`) the JSON
 * text, which ofetch hands back unparsed, read here as the same object; anything that is no object is no error body.
 */
function errorBody(data: unknown): ApiErrorBody | undefined {
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
   * A GET whose body is text, with the reply's headers: `request` returns the body alone. The bearer header goes with it, so a log is
   * fetched, never a link the browser follows (which would carry no token); errors are normalised as `request`'s are.
   */
  async function requestText(path: string, opts: { token?: string; base?: string } = {}): Promise<TextReply> {
    const token = opts.token ?? admin.token.value
    try {
      const res = await $fetch.raw<string>((opts.base ?? httpBase.value) + path, {
        method: 'GET',
        responseType: 'text',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
      return { text: (res._data as string | undefined) ?? '', headers: res.headers }
    } catch (e) {
      throw normalise(e, opts.token !== undefined)
    }
  }

  /**
   * A GET whose body is a file, as a Blob of the type the server gave: a Dispatch attachment, fetched with the bearer header and shown
   * through an object URL, since an `img` or a link the browser follows would carry no token. Errors are normalised as `request`'s are.
   */
  async function requestBlob(path: string, opts: { token?: string; base?: string } = {}): Promise<Blob> {
    const token = opts.token ?? admin.token.value
    try {
      return await $fetch<Blob>((opts.base ?? httpBase.value) + path, {
        method: 'GET',
        responseType: 'blob',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
    } catch (e) {
      throw normalise(e, opts.token !== undefined)
    }
  }

  return { request, requestText, requestBlob }
}
