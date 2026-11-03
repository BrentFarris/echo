import { afterEach, describe, expect, it, vi } from 'vitest';
import { SourceControlView } from './sourceControlView';
import * as api from './sourceControlApi';

vi.mock('./sourceControlApi', () => ({ listRepositories: vi.fn(), loadStatus: vi.fn() }));
vi.mock('./events', () => ({ on: () => () => {}, onState: () => () => {} }));

const controllers: AbortController[] = [];
afterEach(() => {
  controllers.splice(0).forEach(controller => controller.abort());
  document.body.replaceChildren();
  vi.resetAllMocks();
});

function createView() {
  const host = document.createElement('aside');
  document.body.append(host);
  const controller = new AbortController();
  controllers.push(controller);
  const view = new SourceControlView(host, 'echoapp', controller.signal, {
    roots: () => [], openFile: async () => {}, openDiff: async () => {}, updateBadge: () => {},
  });
  return { host, view, controller };
}

describe('source control discovery', () => {
  it('renders immediately while discovery is pending and shares repeated requests', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof api.listRepositories>>) => void;
    vi.mocked(api.listRepositories).mockReturnValue(new Promise(done => { resolve = done; }));
    const { host, view } = createView();
    const first = view.start();
    const second = view.start();
    expect(host.textContent).toContain('SOURCE CONTROL');
    expect(host.textContent).toContain('Discovering repositories');
    expect(host.querySelector('[data-git-global="refresh"]')).not.toBeNull();
    expect(api.listRepositories).toHaveBeenCalledTimes(1);
    resolve({ repositories: [], providers: [], searchParentRepositories: false });
    await Promise.all([first, second]);
    expect(host.textContent).not.toContain('Discovering repositories');
    expect(host.textContent).toContain('No source control repositories');
  });

  it('shows a retryable error when discovery fails', async () => {
    vi.mocked(api.listRepositories).mockRejectedValue(new Error('Git plugin unavailable'));
    const { host, view } = createView();
    await view.start();
    expect(host.textContent).toContain('Git plugin unavailable');
    expect(host.querySelector('button')?.textContent).toBe('Retry');
  });

  it('cancels discovery when its editor session closes', async () => {
    vi.mocked(api.listRepositories).mockImplementation((_id, signal) => new Promise((_resolve, reject) => {
      signal!.addEventListener('abort', () => reject(signal!.reason), { once: true });
    }));
    const { host, view, controller } = createView();
    const started = view.start();
    controller.abort();
    await started;
    expect(host.querySelector('.git-error')).toBeNull();
    expect(vi.mocked(api.listRepositories).mock.calls[0][1]?.aborted).toBe(true);
  });
});
