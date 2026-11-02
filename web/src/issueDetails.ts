export interface IssueDetails {
  issue: number;
  title: string;
  body: string;
  state: string;
  url: string;
}

export class IssueDetailsCache {
  private requests = new Map<number, Promise<IssueDetails>>();
  private fetcher: typeof fetch;

  constructor(fetcher: typeof fetch = fetch) {
    this.fetcher = fetcher;
  }

  async load(issue: number): Promise<IssueDetails> {
    const cached = this.requests.get(issue);
    if (cached) return cached;

    const request = this.fetcher(`/api/issues/${issue}`).then(async (res) => {
      if (!res.ok) throw new Error(`Failed to load issue #${issue}`);
      return (await res.json()) as IssueDetails;
    });
    this.requests.set(issue, request);

    try {
      return await request;
    } catch (error) {
      if (this.requests.get(issue) === request) this.requests.delete(issue);
      throw error;
    }
  }
}

export const issueDetailsCache = new IssueDetailsCache();
