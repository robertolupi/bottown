// The UI's views and their addresses in the URL's fragment (design §4):
// `#/now`, `#/history`, … An empty fragment is Now, and so is one this
// router does not know. The fragments before routes (`#milestones`,
// `#timeline`, `#stats`) still name their views.

export type View = 'now' | 'history' | 'milestones' | 'timeline' | 'stats' | 'repo';

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

// parseRoute returns the view a fragment (location.hash, with or without
// its `#`) names.
export function parseRoute(hash: string): View {
  const h = hash.startsWith('#') ? hash.slice(1) : hash;
  if (h.startsWith('/')) {
    const name = h.slice(1).split(/[/?]/, 1)[0];
    return known.has(name) ? (name as View) : 'now';
  }
  return legacy.has(h) ? (h as View) : 'now';
}

// routeHash returns the fragment, with its `#`, that addresses view.
export function routeHash(view: View): string {
  return `#/${view}`;
}
