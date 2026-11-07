import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '../api/client';
import type { ComponentResponse, EventResponse, LogsResponse } from '../api/types';
import { store } from '../state/store';
import { mkEvent } from '../test-utils';
import { HealthTab } from './HealthTab';
import { LogsTab } from './LogsTab';
import { MessagesTab } from './MessagesTab';
import { OpenIn } from './OpenIn';

const fake = (over: Partial<Client>): Client => over as Client;
const logLine = (id: number, over = {}) =>
  mkEvent(id, { kind: 'log', source: 'tracking', traceId: 't1', spanId: 's1', attrs: { body: `line ${id}`, severity: 'INFO' }, ...over });

beforeEach(() => store.reset());

describe('LogsTab', () => {
  it('logsDefaultToThisOrder', async () => {
    store.selectOrder('d-1');
    const componentLogs = vi.fn(async (): Promise<LogsResponse> => ({ logs: [logLine(1)], next: 0 }));
    render(<LogsTab id="tracking" client={fake({ componentLogs })} />);
    await screen.findByText('line 1');
    expect(componentLogs).toHaveBeenCalledWith('tracking', expect.objectContaining({ deliveryId: 'd-1' }));
    fireEvent.click(screen.getByRole('button', { name: /all of component/i }));
    await waitFor(() => expect(componentLogs).toHaveBeenLastCalledWith('tracking', expect.objectContaining({ deliveryId: undefined })));
    fireEvent.click(screen.getByLabelText(/warn/i));
    await waitFor(() => expect(componentLogs).toHaveBeenLastCalledWith('tracking', expect.objectContaining({ level: 'warn' })));
  });

  it('without an order the logs are the component\'s and there is no toggle', async () => {
    const componentLogs = vi.fn(async (): Promise<LogsResponse> => ({ logs: [], next: 0 }));
    render(<LogsTab id="tracking" client={fake({ componentLogs })} />);
    await waitFor(() => expect(componentLogs).toHaveBeenCalled());
    expect(componentLogs).toHaveBeenCalledWith('tracking', expect.objectContaining({ deliveryId: undefined }));
    expect(screen.queryByRole('button', { name: /all of component/i })).toBeNull();
  });

  it('logLineSelectsItsPulse', async () => {
    store.addEvents([mkEvent(7, { traceId: 't1', spanId: 's1' })]);
    const componentLogs = vi.fn(async (): Promise<LogsResponse> => ({ logs: [logLine(9)], next: 0 }));
    render(<LogsTab id="tracking" client={fake({ componentLogs })} />);
    fireEvent.click(await screen.findByText('line 9'));
    expect(store.getState().selection).toEqual({ kind: 'event', id: '7' });
  });

  it('in replay only lines at or before the cursor show', async () => {
    const a = logLine(1);
    const b = logLine(2_000_000);
    store.setMode('replay');
    store.setReplayAt(Date.parse(a.occurredAt) + 10);
    const componentLogs = vi.fn(async (): Promise<LogsResponse> => ({ logs: [a, b], next: 0 }));
    render(<LogsTab id="tracking" client={fake({ componentLogs })} />);
    await screen.findByText('line 1');
    expect(screen.queryByText('line 2000000')).toBeNull();
  });

  it('renders partner strings as text', async () => {
    const componentLogs = vi.fn(async (): Promise<LogsResponse> => ({ logs: [logLine(1, { attrs: { body: '<img src=x onerror=alert(1)>', severity: 'WARN' } })], next: 0 }));
    const { container } = render(<LogsTab id="tracking" client={fake({ componentLogs })} />);
    await screen.findByText('<img src=x onerror=alert(1)>');
    expect(container.querySelector('img')).toBeNull();
  });
});

describe('MessagesTab', () => {
  it('pulseSelectsItsLogs', async () => {
    const e = mkEvent(7, { traceId: 'abc123', spanId: 's1', outcome: 'failed', attrs: { duration_ms: 42, span_name: 'tracking.ingest' } });
    store.addEvents([e]);
    store.select({ kind: 'event', id: '7' });
    const event = vi.fn(async (): Promise<EventResponse> => ({ event: e, logs: [logLine(8)], links: [] }));
    render(<MessagesTab id="tracking" client={fake({ event })} />);
    expect(await screen.findByText('line 8')).toBeInTheDocument();
    expect(event).toHaveBeenCalledWith(7);
    expect(screen.getByText('abc123')).toBeInTheDocument();
    expect(screen.getByText(/42 ms/)).toBeInTheDocument();
    expect(screen.getByText('failed')).toBeInTheDocument();
  });
});

describe('HealthTab', () => {
  const health: ComponentResponse['health'] = {
    liveness: { state: 'ok' },
    readiness: { state: 'not_probed' },
    dependencies: [{ id: 'redis', state: 'failed' }],
    business: { state: 'warning', reason: '2 recent findings' },
  };
  it('healthLinesSeparate', () => {
    render(<HealthTab health={health} />);
    const live = screen.getByTestId('health-liveness');
    const ready = screen.getByTestId('health-readiness');
    expect(live).toHaveTextContent(/liveness/i);
    expect(live).toHaveTextContent('ok');
    expect(live).not.toHaveTextContent('redis');
    expect(ready).toHaveTextContent('not probed (mTLS)');
    expect(ready.className).toMatch(/is-neutral/);
    expect(ready.className).not.toMatch(/is-fail/);
    expect(screen.getByTestId('health-dependencies')).toHaveTextContent('redis');
    expect(screen.getByTestId('health-business')).toHaveTextContent('2 recent findings');
  });
});

describe('OpenIn', () => {
  it('openInHiddenWithoutLinks', () => {
    const { container } = render(<OpenIn links={[]} />);
    expect(container).toBeEmptyDOMElement();
  });
  it('renders safe anchors with text labels only', () => {
    const { container } = render(<OpenIn links={[{ label: '<img src=x>', url: 'https://grafana.local/d/1' }]} />);
    const a = screen.getByRole('link');
    expect(a).toHaveAttribute('href', 'https://grafana.local/d/1');
    expect(a).toHaveAttribute('target', '_blank');
    expect(a).toHaveAttribute('rel', 'noopener noreferrer');
    expect(container.querySelector('img')).toBeNull();
  });
  it('drops non-http links', () => {
    const { container } = render(<OpenIn links={[{ label: 'x', url: 'javascript:alert(1)' }]} />);
    expect(container).toBeEmptyDOMElement();
  });
});
