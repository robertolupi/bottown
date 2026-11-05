// The layout metrics `bottown web shot` records for each page it captures,
// evaluated in the page as (this function)(options). It returns plain JSON:
// the panels in document order, every element that can scroll or has a
// max-height, whatever overflows the viewport sideways, and the elements
// the caller's selectors match.
(opts) => {
  const de = document.documentElement;
  const round = (n) => Math.round(n * 10) / 10;
  // Svelte's scoping classes (svelte-xyz) change with every build.
  const classes = (el) => [...el.classList].filter((c) => !c.startsWith('svelte-'));
  const label = (el) => {
    let s = el.tagName.toLowerCase();
    if (el.id) s += '#' + el.id;
    for (const c of classes(el)) s += '.' + c;
    return s;
  };
  const heading = (el) => {
    const h = el && el.querySelector('h1, h2, h3');
    return h ? h.textContent.trim().replace(/\s+/g, ' ') : null;
  };
  const scrolls = (v) => v === 'auto' || v === 'scroll';
  const describe = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return {
      el: label(el),
      panel: heading(el.closest('section')),
      box: { x: round(r.left + scrollX), y: round(r.top + scrollY), w: round(r.width), h: round(r.height) },
      scrollH: el.scrollHeight,
      clientH: el.clientHeight,
      scrollW: el.scrollWidth,
      clientW: el.clientWidth,
      overflowY: cs.overflowY,
      overflowX: cs.overflowX,
      maxH: cs.maxHeight,
      scrollsY: scrolls(cs.overflowY) && el.scrollHeight > el.clientHeight,
      scrollsX: scrolls(cs.overflowX) && el.scrollWidth > el.clientWidth,
      visible: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden',
    };
  };

  const all = [...document.body.querySelectorAll('*')];
  const scrollers = all.filter((el) => {
    const cs = getComputedStyle(el);
    return scrolls(cs.overflowY) || scrolls(cs.overflowX) || cs.maxHeight !== 'none';
  });

  // The outermost elements that reach past the viewport's right edge
  // without a clipping ancestor: what makes a narrow page scroll sideways.
  const clips = (el) => {
    const cs = getComputedStyle(el);
    return cs.overflowX !== 'visible';
  };
  const wide = [];
  for (const el of all) {
    if (el.getBoundingClientRect().right <= de.clientWidth + 0.5) continue;
    let clipped = false;
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      if (clips(p) || wide.includes(p)) {
        clipped = true;
        break;
      }
    }
    if (!clipped) wide.push(el);
    if (wide.length >= 20) break;
  }

  const selected = {};
  for (const s of opts.selectors || []) {
    try {
      selected[s] = [...document.querySelectorAll(s)].map(describe);
    } catch (e) {
      selected[s] = { error: String(e) };
    }
  }

  const panels = [...document.querySelectorAll('section')];
  return {
    url: location.href,
    title: document.title,
    viewport: { w: innerWidth, h: innerHeight },
    scheme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
    page: {
      scrollW: de.scrollWidth,
      scrollH: de.scrollHeight,
      clientW: de.clientWidth,
      overflowX: de.scrollWidth > de.clientWidth,
    },
    order: panels.map(heading),
    panels: panels.map(describe),
    scrollers: scrollers.map(describe),
    overflowX: wide.map(describe),
    selected,
  };
}
