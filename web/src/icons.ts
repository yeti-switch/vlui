// The shapes a tool's `icon:` can name.
//
// Inline SVG bodies rather than an icon package: there are a dozen of them, they
// never change, and a dependency would be several hundred kilobytes to draw a
// gear. Every one is drawn on a 24x24 grid with currentColor strokes, so they
// inherit the rail's colour and the theme with it.
//
// The names must match config.Icons on the Go side, which validates them at
// startup — TestIconsMatchTheFrontend fails if the two lists drift apart.
export const ICONS: Record<string, string> = {
  gear: `<circle cx="12" cy="12" r="3.2"/><path d="M19.4 14a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V20a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 9 18.4a1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 9a1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1Z"/>`,

  // The yeti-switch mark: a shield with a check, as in the other yeti tools.
  yeti: `<path d="M12 2 3 7v10l9 5 9-5V7l-9-5Z" stroke-linejoin="round"/><path d="M7 14.5 10 11l2.5 2.5L17 8.5" stroke-linecap="round" stroke-linejoin="round"/>`,

  bolt: `<path d="M13 2 4.5 13.5H11L10 22l8.5-11.5H12L13 2Z" stroke-linejoin="round"/>`,

  bug: `<path d="M8 7a4 4 0 0 1 8 0"/><rect x="6" y="7" width="12" height="12" rx="5"/><path d="M3 11h3M18 11h3M3 17h3.5M17.5 17H21M12 19v3M9.5 4.5 8 3M14.5 4.5 16 3"/>`,

  chart: `<path d="M4 20V4M4 20h16"/><rect x="7" y="12" width="3" height="5"/><rect x="12" y="8" width="3" height="9"/><rect x="17" y="5" width="3" height="12"/>`,

  cloud: `<path d="M7.5 19a4.5 4.5 0 0 1-.4-9 6 6 0 0 1 11.6 1.6A3.9 3.9 0 0 1 17.5 19h-10Z" stroke-linejoin="round"/>`,

  database: `<ellipse cx="12" cy="6" rx="7.5" ry="3"/><path d="M4.5 6v12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3V6"/><path d="M4.5 12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3"/>`,

  globe: `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.6 3.8 5.7 3.8 9S14.5 18.4 12 21c-2.5-2.6-3.8-5.7-3.8-9S9.5 5.6 12 3Z"/>`,

  lock: `<rect x="4.5" y="10" width="15" height="10.5" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/>`,

  phone: `<path d="M7 3h3l1.5 4.5-2 1.5a12 12 0 0 0 5.5 5.5l1.5-2L21 14v3a2 2 0 0 1-2.2 2A16.5 16.5 0 0 1 5 5.2 2 2 0 0 1 7 3Z" stroke-linejoin="round"/>`,

  server: `<rect x="3.5" y="4" width="17" height="6.5" rx="1.6"/><rect x="3.5" y="13.5" width="17" height="6.5" rx="1.6"/><path d="M7 7.2h.01M7 16.7h.01" stroke-linecap="round"/>`,

  tag: `<path d="M3.5 11.5V4.5a1 1 0 0 1 1-1h7l9 9-8 8-9-9Z" stroke-linejoin="round"/><circle cx="8" cy="8" r="1.4"/>`,

  terminal: `<rect x="3" y="4.5" width="18" height="15" rx="2"/><path d="m7.5 9.5 3 2.5-3 2.5M13 15h4" stroke-linecap="round" stroke-linejoin="round"/>`,
}

// A name the Go side accepted but this map somehow lacks would render an empty
// square; a tag is a better answer than a hole.
export function iconBody(name: string): string {
  return ICONS[name] ?? ICONS.tag!
}
