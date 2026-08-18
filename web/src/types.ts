// A log entry as VictoriaLogs returns it: every field is a string, and which
// fields exist varies from line to line.
export type LogRow = Record<string, string>

export interface Preset {
  name: string
  query: string
}

// One icon in the left rail. Its query narrows everything the session asks for
// — and it is the SERVER that applies it, from the id, on every request. What
// arrives here is a label to show beside the input, not the mechanism.
export interface Tool {
  id: string
  tooltip: string
  icon: string
  query: string
}

export interface User {
  sub: string
  email?: string
  name?: string
  groups?: string[]
}

// What GET /api/config answers: everything the SPA cannot work out for itself.
export interface AppConfig {
  version: string
  commit: string
  base_path: string
  auth_enabled: boolean
  user: User | null
  default_limit: number
  max_rows: number
  default_range_seconds: number
  tail_max_seconds: number
  queries: Preset[]
  tools: Tool[]
}

export interface HitsSeries {
  fields: Record<string, string>
  timestamps: string[]
  values: number[]
  total: number
}

export interface HitsResponse {
  hits: HitsSeries[]
  step_seconds: number
}

export interface FacetValue {
  field_value: string
  hits: number
}

export interface Facet {
  field_name: string
  values: FacetValue[]
}

export interface FacetsResponse {
  facets: Facet[]
}

export interface ValuesResponse {
  values: { value: string; hits: number }[]
}

// An absolute window. The UI works in relative terms ("last 15 minutes") but
// every request is sent as two instants, so what was asked for stays fixed
// while the query runs.
export interface TimeRange {
  startMs: number
  endMs: number
}
