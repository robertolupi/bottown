// LatestLoad keeps a view's one request in flight for the range it shows
// (issue #355). A load for the range already loading is dropped, as a poll
// or a refresh during a slow load would only repeat it; a load for another
// range aborts the running one, so the view never draws an old range under
// the new selector. The range is the selection (preset and custom dates),
// not the query: a preset's query moves with the clock.
export class LatestLoad {
  #controller: AbortController | null = null;
  #range: string | null = null;

  // start begins a load of range and returns its signal, or null when that
  // range is already loading. It aborts a running load of another range.
  start(range: string): AbortSignal | null {
    if (this.#controller && this.#range === range) return null;
    this.#controller?.abort();
    this.#controller = new AbortController();
    this.#range = range;
    return this.#controller.signal;
  }

  // cancel aborts the running load, if any, and reports whether there was one.
  cancel(): boolean {
    if (!this.#controller) return false;
    this.#controller.abort();
    this.#controller = null;
    this.#range = null;
    return true;
  }

  // finish ends signal's load and reports whether it was still the current
  // one; a load that another started or cancel aborted must not touch the view.
  finish(signal: AbortSignal): boolean {
    if (this.#controller?.signal !== signal) return false;
    this.#controller = null;
    this.#range = null;
    return true;
  }
}
