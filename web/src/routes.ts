// The UI's views and their addresses in the URL's fragment (design §4):
// `#/now`, `#/history`, … An empty fragment is Now, and so is one this
// router does not know. The fragments before routes (`#milestones`,
// `#timeline`, `#stats`) still name their views.
// Routes `#/run/<id>` and `#/flow/<issue>` (optionally `?pr=N`) address
// the run and flow page.

export type View = 'now' | 'history' | 'milestones' | 'timeline' | 'stats' | 'repo';

export type Route =
  | { kind: 'view'; view: View }
  | { kind: 'run'; id: string }
  | { kind: 'flow'; issue: number; pr?: number };

export const VIEWS: { view: View; label: string }[] = [
  { view: 'now', label: 'Now' },
  { view: 'history', label: 'History' },
  { view: 'milestones', label: 'Milestones' },
  { view: 'timeline', label: 'Timeline' },
  { view: 'stats', label: 'Stats' },
  { view: 'repo', label: 'Repository' },
];

const known = new Set<string>(VIEWS.map((v) => v.view));
const legacy = new Set<string>(['milestones', 'timeline', 'stats']);

// parseRoute returns the Route a fragment (location.hash, with or without
// its `#`) names.
export function parseRoute(hash: string): Route {
  const h = hash.startsWith('#') ? hash.slice(1) : hash;
  if (h.startsWith('/run/')) {
    const id = h.slice('/run/'.length).split(/[?#/]/)[0];
    if (id) return { kind: 'run', id };
    return { kind: 'view', view: 'now' };
  }
  if (h.startsWith('/flow/')) {
    const rest = h.slice('/flow/'.length);
    const [issueStr, query] = rest.split('?');
    const issue = parseInt(issueStr, 10);
    if (!isNaN(issue)) {
      let pr: number | undefined;
      if (query) {
        const params = new URLSearchParams(query);
        const p = params.get('pr');
        if (p !== null) {
          const parsedPr = parseInt(p, 10);
          if (!isNaN(parsedPr)) pr = parsedPr;
        }
      }
      return { kind: 'flow', issue, pr };
    }
    return { kind: 'view', view: 'now' };
  }
  if (h.startsWith('/')) {
    const name = h.slice(1).split(/[/?]/, 1)[0];
    return known.has(name) ? { kind: 'view', view: name as View } : { kind: 'view', view: 'now' };
  }
  return legacy.has(h) ? { kind: 'view', view: h as View } : { kind: 'view', view: 'now' };
}

// routeHash returns the fragment, with its `#`, that addresses view.
export function routeHash(view: View): string {
  return `#/${view}`;
}

// runRouteHash returns the fragment that addresses a run's page.
export function runRouteHash(id: string): string {
  return `#/run/${id}`;
}

// flowRouteHash returns the fragment that addresses a flow's page.
export function flowRouteHash(issue: number, pr?: number): string {
  const query = pr !== undefined && pr > 0 ? `?pr=${pr}` : '';
  return `#/flow/${issue}${query}`;
}
