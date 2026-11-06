// Mirrors the JSON of observability/mission-control/backend/internal/api (dto.go, handlers.go, api.go,
// sse.go). Every string is data from outside: render it as text, never as HTML.

// request | message_published | message_consumed | state_change | log | health | detector (open set)
export type EventKind = string;
export type Origin = 'live' | 'simulated' | 'replay' | string;

export interface McEvent {
  id: number;
  observedAt: string;
  occurredAt: string;
  kind: EventKind;
  source: string;
  target: string;
  outcome: string; // ok | failed | slow | retried | dropped | rejected | ... (open set)
  reason: string;
  tenantId: string;
  environment: string;
  deliveryId: string;
  orderRef: string;
  traceId: string;
  spanId: string;
  origin: Origin;
  attrs: Record<string, unknown>;
}

export interface OrderHit {
  tenantId: string;
  environment: string;
  orderRef: string;
  deliveryId: string;
  origin: Origin;
  createdAt: string;
}

// topology.Component and topology.Wire carry no json tags in Go, so the keys are the Go field names.
export interface Component {
  ID: string;
  Label: string;
  Zone: 'outside' | 'edge' | 'services' | 'messaging' | 'data';
  Parent: string;
  Instrumented: boolean;
  Status: '' | 'not_built';
}

export interface Wire {
  ID: string;
  From: string;
  To: string;
  Channel: 'rpc' | 'nats' | 'redis' | 'http' | 'ws_sse' | 'sql' | 'osrm';
}

export type ComponentState =
  | 'not_built'
  | 'not_instrumented'
  | 'unreachable'
  | 'warning'
  | 'active'
  | 'idle'
  | 'stale';

export interface ComponentWithState extends Component {
  state: ComponentState;
  lastSeen: string | null;
}

export interface TopologyResponse {
  components: ComponentWithState[];
  wires: Wire[];
}

// not_probed: Mission Control cannot ask (deliveries, accounts: mTLS-only health, ruling R22). Never red.
export type HealthState = 'ok' | 'failed' | 'not_probed' | 'unknown' | 'warning';

export interface HealthField {
  state: HealthState;
  reason?: string;
  at?: string;
}

export interface Link {
  label: string;
  url: string;
}

export interface ComponentResponse {
  component: Component;
  health: {
    liveness: HealthField;
    readiness: HealthField;
    dependencies: { id: string; state: HealthState }[];
    business: HealthField;
  };
  recent: McEvent[];
  links: Link[];
}

export interface LogsResponse {
  logs: McEvent[];
  next: number;
}

export interface EventResponse {
  event: McEvent;
  logs: McEvent[];
  links: Link[];
}

export interface StatusStep {
  status: string;
  at: string | null;
  state: 'done' | 'warning' | 'pending';
}

export interface OrderResponse {
  order: OrderHit;
  statusSteps: StatusStep[];
  detectors: McEvent[];
}

export interface TimelineGap {
  from: string;
  to: string;
  kind: 'mc_down' | 'no_trace';
}

export interface TimelineResponse {
  events: McEvent[];
  next: number; // 0 = no more pages
  gaps: TimelineGap[];
}

export interface WireTraffic {
  wire: string;
  count: number;
  failed: number;
  rate: number;
  errorRate: number;
  p95Ms: number;
}

export interface SystemResponse {
  traffic: WireTraffic[];
  recentOrders: OrderHit[];
  warnings: McEvent[];
}

export interface SelfRow {
  source: string;
  lastSeen: string;
  lagMs: number;
  drops: number;
  redacted: number;
  storageErrors: number;
  metricsSourceUnavailable: boolean;
}

// ---- SSE messages ---------------------------------------------------------

export interface GapData {
  from: number;
  to: number;
}

export interface BundleData {
  wire: string; // "source>target", or the source when there is no target
  count: number;
  outcome: string;
}

export type StreamMessage =
  | { type: 'event'; id: number; data: McEvent }
  | { type: 'detector'; id: number; data: McEvent }
  | { type: 'gap'; id: number; data: GapData }
  | { type: 'bundle'; id: number; data: BundleData };
