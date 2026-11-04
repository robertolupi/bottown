// Whether a one-line, ellipsis-clipped history summary hides text, so its
// cell offers to show the rest inline (#133) instead of only in a title.

// The parts of an element truncation looks at; an HTMLElement has them.
export interface Clippable {
  scrollWidth: number;
  clientWidth: number;
}

// isTruncated: the text is wider than its box. A pixel of slack absorbs
// subpixel rounding, which would otherwise offer "more" on text that fits.
export function isTruncated(el: Clippable): boolean {
  return el.scrollWidth > el.clientWidth + 1;
}

// anyTruncated: any of the elements hides text.
export function anyTruncated(els: Iterable<Clippable>): boolean {
  for (const el of els) if (isTruncated(el)) return true;
  return false;
}

// TruncationWatch is watchTruncation's parameter: key is what the clipped
// text depends on (its text, whether it is expanded), so a change to it
// measures again even when no box resizes.
export interface TruncationWatch {
  key: unknown;
  onChange: (truncated: boolean) => void;
}

// watchTruncation is a Svelte action: it calls onChange with whether any
// `.clip` element inside node hides text, now, whenever node or a clipped
// element resizes (a narrower window clips more), and when key changes.
export function watchTruncation(node: HTMLElement, watch: TruncationWatch) {
  let cb = watch.onChange;
  const clips = () => node.querySelectorAll<HTMLElement>('.clip');
  const measure = () => cb(anyTruncated(clips()));
  const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
  const observe = () => {
    if (!ro) return;
    ro.disconnect();
    ro.observe(node);
    clips().forEach((el) => ro.observe(el));
  };
  observe();
  measure();
  return {
    update(next: TruncationWatch) {
      cb = next.onChange;
      observe();
      measure();
    },
    destroy() {
      ro?.disconnect();
    },
  };
}
