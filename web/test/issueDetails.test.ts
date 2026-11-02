import test from 'node:test';
import assert from 'node:assert/strict';
import { IssueDetailsCache, type IssueDetails } from '../src/issueDetails.ts';

const details: IssueDetails = {
  issue: 129,
  title: 'Show issue titles',
  body: 'Description',
  state: 'open',
  url: 'http://forge/issues/129',
};

function response(body: unknown, ok = true): Response {
  return {
    ok,
    json: async () => body,
  } as Response;
}

test('IssueDetailsCache deduplicates concurrent loads and caches successful details', async () => {
  let calls = 0;
  let finish: ((res: Response) => void) | undefined;
  const cache = new IssueDetailsCache(async (input) => {
    calls++;
    assert.equal(input, '/api/issues/129');
    return new Promise<Response>((resolve) => {
      finish = resolve;
    });
  });

  const first = cache.load(129);
  const second = cache.load(129);
  assert.equal(calls, 1);
  finish!(response(details));
  assert.deepEqual(await first, details);
  assert.deepEqual(await second, details);

  assert.deepEqual(await cache.load(129), details);
  assert.equal(calls, 1);
});

test('IssueDetailsCache isolates issues and retries a failed load without caching the failure', async () => {
  let calls = 0;
  const cache = new IssueDetailsCache(async (input) => {
    calls++;
    if (input === '/api/issues/2') return response({ error: 'unavailable' }, false);
    return response({ ...details, issue: 1 });
  });

  await assert.rejects(cache.load(2), /Failed to load issue #2/);
  await assert.rejects(cache.load(2), /Failed to load issue #2/);
  assert.equal(calls, 2);
  assert.equal((await cache.load(1)).issue, 1);
  assert.equal(calls, 3);
});
