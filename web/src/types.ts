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
  // One or the other: a shape from icons.ts, or up to three characters drawn in
  // its place. The config refuses both.
  icon: string
  letters: string
  query: string
  // The columns this tool opens with, from the config. A default: whatever the
  // reader picks afterwards is remembered in their browser and wins.
  fields: Field[]
}

// A column: the log field, and what to call it in the table header. A field
// name is often far wider than its values, and the header is what sizes the
// column — the label is how a deployment says "call this one `method`".
export interface Field {
  name: string
  label?: string
  // Names one of AppConfig.value_styles, which decides how this field's values
  // are drawn. Absent means plain text.
  style?: string
}

/* One named way of drawing values: how, and which value gets which colour.
 *
 * `tag` puts the value in a tinted pill — a category, read as a badge. `text`
 * colours the value itself — a measurement that is merely alarming past some
 * point, where a column of pills would read as categories and the digits would
 * stop lining up. The server fills in the type, so it is never absent here. */
export interface ValueStyle {
  type: StyleType
  rules: StyleRule[]
}

export type StyleType = 'tag' | 'text'

/* One rule of a value style: what to match, and the colour a matching value is
 * drawn in. Exactly one matcher is set — the server refuses anything else — and
 * a style's rules are tried in order, first match wins.
 *
 * `range` arrives already parsed: the server validated "200-399" and sends the
 * numbers, so there is one place that decides what a range means. */
export interface StyleRule {
  value?: string[]
  // Either end may be absent, which is an OPEN end rather than a zero: {lo:
  // 1000} is "a thousand and up", which is what a threshold is.
  range?: { lo?: number; hi?: number }
  prefix?: string
  default?: boolean
  color: StyleColor
  // What a matching value means, shown under the value in the cell's tooltip.
  // Absent when the value speaks for itself.
  description?: string
}

// The colours the stylesheet defines, in both themes. Anything else is refused
// at startup, so this list and the CSS are the same list.
export type StyleColor = 'ok' | 'warn' | 'error' | 'info' | 'neutral' | 'muted'

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
  // The value styles a tool's fields refer to by name. Absent when no tool this
  // account can see uses one.
  value_styles?: Record<string, ValueStyle>
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
