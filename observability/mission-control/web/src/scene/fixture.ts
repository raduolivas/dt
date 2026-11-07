// Test fixture: DT's default topology as /api/topology returns it (mirrors
// backend/internal/topology/topology.go Default()). Test-only.
import type { Component, ComponentWithState, TopologyResponse, Wire } from '../api/types';

const comps: Component[] = [];
const add = (id: string, label: string, zone: Component['zone'], parent = '', instrumented = true, status: Component['status'] = '') =>
  comps.push({ id, label, zone, parent, instrumented, status });
const parts = (parent: string, zone: Component['zone'], ...names: string[]) =>
  names.forEach((n) => add(`${parent}.${n}`, n, zone, parent));

add('partner', 'Partner', 'outside', '', false);
add('courier', 'Courier', 'outside', '', false);
add('viewer', 'Viewer', 'outside', '', false);
add('gateway', 'Gateway', 'edge');
parts('gateway', 'edge', 'partner_api', 'courier_api', 'viewer_api', 'rate_limiter', 'outbox', 'consumers');
add('deliveries', 'Deliveries', 'services');
parts('deliveries', 'services', 'rpc', 'state_machine', 'outbox', 'consumers');
add('tracking', 'Tracking', 'services');
parts('tracking', 'services', 'ingest', 'consumers', 'state', 'hub', 'sweeper', 'outbox', 'simulator');
add('accounts', 'Accounts', 'services');
add('webhooks', 'Webhooks', 'services', '', false, 'not_built');
add('nats', 'NATS', 'messaging');
for (const s of ['ACCOUNTS', 'DELIVERIES', 'TRACKING', 'GATEWAY']) add(`nats.${s}`, s, 'messaging', 'nats');
add('redis', 'Redis', 'data');
add('pg.deliveries', 'Postgres deliveries', 'data', '', false);
add('pg.tracking', 'Postgres tracking', 'data', '', false);
add('pg.accounts', 'Postgres accounts', 'data', '', false);
add('pg.gateway', 'Postgres gateway', 'data', '', false);
add('osrm', 'OSRM', 'data', '', false);

const wires: Wire[] = [];
const w = (from: string, to: string, channel: Wire['channel']) => wires.push({ id: `${from}>${to}`, from, to, channel });
w('partner', 'gateway.partner_api', 'http');
w('courier', 'gateway.courier_api', 'http');
w('viewer', 'gateway.viewer_api', 'http');
w('viewer', 'tracking.hub', 'ws_sse');
w('gateway.rate_limiter', 'redis', 'redis');
w('gateway.partner_api', 'deliveries.rpc', 'rpc');
w('gateway.partner_api', 'tracking.ingest', 'rpc');
w('gateway.courier_api', 'deliveries.rpc', 'rpc');
w('gateway.courier_api', 'tracking.ingest', 'rpc');
w('gateway.courier_api', 'gateway.outbox', 'sql');
w('gateway.outbox', 'pg.gateway', 'sql');
w('gateway.outbox', 'nats.GATEWAY', 'nats');
w('nats.ACCOUNTS', 'gateway.consumers', 'nats');
w('nats.DELIVERIES', 'gateway.consumers', 'nats');
w('gateway.consumers', 'pg.gateway', 'sql');
w('deliveries.rpc', 'deliveries.state_machine', 'rpc');
w('deliveries.state_machine', 'pg.deliveries', 'sql');
w('deliveries.state_machine', 'deliveries.outbox', 'sql');
w('deliveries.outbox', 'nats.DELIVERIES', 'nats');
w('nats.TRACKING', 'deliveries.consumers', 'nats');
w('nats.ACCOUNTS', 'deliveries.consumers', 'nats');
w('deliveries.consumers', 'deliveries.state_machine', 'sql');
w('tracking.simulator', 'deliveries.rpc', 'rpc');
w('tracking.ingest', 'tracking.state', 'redis');
w('tracking.ingest', 'osrm', 'osrm');
w('tracking.ingest', 'tracking.outbox', 'sql');
w('tracking.outbox', 'pg.tracking', 'sql');
w('tracking.outbox', 'nats.TRACKING', 'nats');
w('tracking.state', 'redis', 'redis');
w('redis', 'tracking.hub', 'redis');
w('tracking.sweeper', 'tracking.state', 'redis');
w('nats.DELIVERIES', 'tracking.consumers', 'nats');
w('nats.ACCOUNTS', 'tracking.consumers', 'nats');
w('tracking.consumers', 'tracking.state', 'redis');
w('tracking.consumers', 'pg.tracking', 'sql');
w('accounts', 'pg.accounts', 'sql');
w('accounts', 'nats.ACCOUNTS', 'nats');

export function topologyFixture(state: ComponentWithState['state'] = 'idle'): TopologyResponse {
  return {
    components: comps.map((c) => ({
      ...c,
      state: c.status === 'not_built' ? 'not_built' : c.instrumented ? state : 'not_instrumented',
      lastSeen: null,
    })),
    wires: wires.map((x) => ({ ...x })),
  };
}

export function comp(id: string, over: Partial<ComponentWithState> = {}): ComponentWithState {
  const c = topologyFixture().components.find((x) => x.id === id);
  if (!c) throw new Error(`no component ${id}`);
  return { ...c, ...over };
}
