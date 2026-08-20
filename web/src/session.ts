// Whether anyone is signed in, and how to change that.
//
// A module rather than state inside App.vue, because a 401 can arrive from any
// request at any time — a session that expired while a tab sat open, or a
// cookie dropped by Sign out — and every one of them has to reach the same
// screen.
import { ref } from 'vue'

// True once the server has told us nobody is signed in. The app shows the
// login gate and nothing else while it is set.
export const signedOut = ref(false)

// Where the OIDC flow starts. The server hands it over with the 401, because
// only it knows the base path it is mounted under; the fallback is for the
// paths where we decide the session is over without asking (Sign out).
const loginPath = ref('')

export function loginURL(): string {
  return loginPath.value || new URL('api/auth/login', document.baseURI).toString()
}

// markSignedOut is called from the API layer on any 401. It does NOT navigate:
// bouncing straight to the IdP is what made "Sign out" appear to do nothing —
// the provider still had a session, signed the browser back in, and the
// operator landed where they started having asked to leave.
export function markSignedOut(url?: string) {
  if (url) loginPath.value = url
  signedOut.value = true
}

// begin sends the browser to the provider, remembering where to come back to.
export function begin() {
  window.location.assign(`${loginURL()}?return_to=${encodeURIComponent(returnTo())}`)
}

// Where to come back to, expressed the way the server expects it: relative to
// the app's own mount point, not to the origin.
//
// The server prepends base_path to whatever it is given. Sending the browser's
// full pathname — which already begins with base_path — therefore lands you at
// /logs/logs/ after signing in. The base comes off here, where document.baseURI
// already knows what it is.
function returnTo(): string {
  const base = new URL(document.baseURI).pathname // "/" or "/logs/"
  let path = window.location.pathname

  if (base !== '/' && path.startsWith(base)) {
    path = '/' + path.slice(base.length)
  }
  return path + window.location.search + window.location.hash
}

// probe asks the server who we are, and raises the gate if the answer is
// nobody.
//
// It exists for EventSource, which cannot report an HTTP status: a live tail
// whose session has expired is refused with a 401, and per the spec the browser
// then closes the stream permanently and hands us an `error` event with nothing
// in it. That is indistinguishable from a network blip until somebody asks —
// so this asks.
export async function probe(): Promise<void> {
  try {
    const res = await fetch(new URL('api/auth/me', document.baseURI), { credentials: 'same-origin' })
    if (res.status !== 401) return

    const body = await res.json().catch(() => ({}))
    markSignedOut(typeof body?.login_url === 'string' ? body.login_url : undefined)
  } catch {
    // The server is unreachable, which is an outage rather than a logout.
    // Saying "please log in" to that would send the operator somewhere that
    // cannot help them either.
  }
}
