import type { McEvent } from './api/types';

export function mkEvent(id: number, over: Partial<McEvent> = {}): McEvent {
  const t = new Date(1_700_000_000_000 + id).toISOString();
  return {
    id, observedAt: t, occurredAt: t, kind: 'request', source: 'gateway', target: 'tracking.ingest', outcome: 'ok',
    reason: '', tenantId: '', environment: '', deliveryId: '', orderRef: '', traceId: '', spanId: '', origin: 'live',
    attrs: {}, ...over,
  };
}
