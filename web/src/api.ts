import type {
  AppConfig,
  FacetsResponse,
  HitsResponse,
  LogRow,
  TimeRange,
  ValuesResponse,
} from './types'

// The sentinel the server appends when a query fails after the response has
// already started. Once the first row is on the wire the status code is spent,
// so an in-band line is the only way left to say so. Must match
// internal/api/query.go.
const ERROR_FIELD = '_vlui_error'

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

// Every request is resolved against <base href>, which the Go server injects
// into index.html. That is what lets the same build serve from / or from /logs.
function apiURL(path: string, params?: Record<string, string | number | undefined>): string {
  const url = new URL(`api/${path}`, document.baseURI)
  for (const [k, v] of Object.entries(params ?? {})) {
    if (v !== undefined && v !== '') url.searchParams.set(k, String(v))
  }
  return url.toString()
}

// The tool is sent as an id, never as its query: the filter it stands for is
// applied server-side, so what the browser can influence is which of the tools
// it was offered is in force — not what the filter says.
function toolParam(tool: string): Record<string, string> {
  return tool ? { tool } : {}
}

function rangeParams(range: TimeRange): Record<string, string> {
  // Unix milliseconds: what Date.getTime() gives, and what the server parses
  // alongside RFC3339.
  return { start: String(range.startMs), end: String(range.endMs) }
}

// A 401 is not an error to show; it is a login that has to happen. The server
// hands back where to go, since only it knows the mount point.
async function check(res: Response): Promise<Response> {
  if (res.ok) return res

  let message = `HTTP ${res.status}`
  let loginURL: string | undefined
  try {
    const body = await res.json()
    if (typeof body?.error === 'string') message = body.error
    if (typeof body?.login_url === 'string') loginURL = body.login_url
  } catch {
    // A proxy in front of us may answer with HTML; the status is all we get.
  }

  if (res.status === 401 && loginURL) {
    const back = window.location.pathname + window.location.search + window.location.hash
    window.location.assign(`${loginURL}?return_to=${encodeURIComponent(back)}`)
    // The navigation is asynchronous; nothing after this should render.
    throw new ApiError('signing in…', 401)
  }

  throw new ApiError(message, res.status)
}

async function getJSON<T>(path: string, params: Record<string, string | number | undefined>, signal?: AbortSignal): Promise<T> {
  const res = await check(await fetch(apiURL(path, params), { signal, credentials: 'same-origin' }))
  return (await res.json()) as T
}

export function fetchConfig(): Promise<AppConfig> {
  return getJSON<AppConfig>('config', {})
}

export function fetchHits(query: string, range: TimeRange, tool: string, signal?: AbortSignal): Promise<HitsResponse> {
  return getJSON<HitsResponse>('hits', { query, ...toolParam(tool), ...rangeParams(range) }, signal)
}

export function fetchFacets(query: string, range: TimeRange, limit: number, tool: string, signal?: AbortSignal): Promise<FacetsResponse> {
  return getJSON<FacetsResponse>('facets', { query, limit, ...toolParam(tool), ...rangeParams(range) }, signal)
}

export function fetchFieldNames(query: string, range: TimeRange, filter: string, tool: string, signal?: AbortSignal): Promise<ValuesResponse> {
  return getJSON<ValuesResponse>('field_names', { query, filter, ...toolParam(tool), ...rangeParams(range) }, signal)
}

export function fetchFieldValues(query: string, field: string, range: TimeRange, filter: string, tool: string, signal?: AbortSignal): Promise<ValuesResponse> {
  return getJSON<ValuesResponse>('field_values', { query, field, filter, ...toolParam(tool), ...rangeParams(range) }, signal)
}

export interface QueryRequest {
  query: string
  range: TimeRange
  limit: number
  tool: string
}

// streamQuery yields rows in batches as they arrive, so the table fills while
// the query is still running rather than after it. Batching rather than
// yielding one row at a time keeps Vue from re-rendering thousands of times.
export async function* streamQuery(req: QueryRequest, signal: AbortSignal): AsyncGenerator<LogRow[]> {
  const body = new URLSearchParams({
    query: req.query,
    limit: String(req.limit),
    ...toolParam(req.tool),
    ...rangeParams(req.range),
  })

  const res = await check(
    await fetch(apiURL('query'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body,
      signal,
      credentials: 'same-origin',
    }),
  )
  if (!res.body) return

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let batch: LogRow[] = []

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })

      // The last element is whatever came after the final newline: an
      // incomplete line, kept for the next chunk.
      const lines = buffer.split('\n')
      buffer = lines.pop() ?? ''

      for (const line of lines) {
        const row = parseRow(line)
        if (row) batch.push(row)
      }
      if (batch.length > 0) {
        yield batch
        batch = []
      }
    }
  } finally {
    // Cancelling the reader is what closes the connection, which is what tells
    // the server — and through it VictoriaLogs — to abandon the query.
    await reader.cancel().catch(() => {})
  }

  const last = parseRow(buffer)
  if (last) batch.push(last)
  if (batch.length > 0) yield batch
}

function parseRow(line: string): LogRow | null {
  const trimmed = line.trim()
  if (!trimmed) return null

  let row: LogRow
  try {
    row = JSON.parse(trimmed) as LogRow
  } catch {
    // A malformed line means the stream is no longer JSON — worth surfacing
    // rather than silently dropping.
    throw new ApiError(`unreadable line from VictoriaLogs: ${trimmed.slice(0, 200)}`, 502)
  }

  const failure = row[ERROR_FIELD]
  if (typeof failure === 'string') throw new ApiError(failure, 502)
  return row
}

export interface TailHandlers {
  onRow: (row: LogRow) => void
  onError: (message: string) => void
}

// openTail follows new logs over Server-Sent Events. EventSource reconnects by
// itself, which is what makes the server's tail ceiling invisible to the user.
export function openTail(query: string, tool: string, handlers: TailHandlers): EventSource {
  const es = new EventSource(apiURL('tail', { query, ...toolParam(tool) }), { withCredentials: true })

  es.onmessage = (ev) => {
    try {
      handlers.onRow(JSON.parse(ev.data) as LogRow)
    } catch {
      // A frame we cannot read is one line lost; the tail keeps running.
    }
  }

  es.addEventListener('error', (ev) => {
    const data = (ev as MessageEvent).data
    if (typeof data === 'string' && data) {
      try {
        handlers.onError(String(JSON.parse(data).error ?? data))
        return
      } catch {
        handlers.onError(data)
        return
      }
    }
    // No payload means the transport dropped. EventSource is already retrying,
    // so this is a status, not a failure.
  })

  return es
}
