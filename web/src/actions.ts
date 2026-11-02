import type { ActionResult } from './types';

// The Start Flow dialog's values (buildFlowActionPayload's input).
export interface FlowForm {
  issue: number | string;
  implement: string;
  verify: string;
  rounds: number;
  timeout: string;
}

// The Start Run dialog's values (buildRunActionPayload's input).
export interface RunForm {
  kind: 'implement' | 'verify' | 'discuss';
  target: number | string;
  actor: string;
  question: string;
  allowSameFamily: boolean;
}

// postAction POSTs an owner action to the UI server (/api/actions/...),
// with body as JSON when given, and returns its result. It throws when the
// action failed, with the server's error, else its output, else failure.
export async function postAction(path: string, body: unknown, failure: string): Promise<ActionResult> {
  const init: RequestInit =
    body === undefined
      ? { method: 'POST' }
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
  const res = await fetch(path, init);
  const data: ActionResult = await res.json();
  if (!res.ok || !data.success) {
    throw new Error(data.error || data.output || failure);
  }
  return data;
}
