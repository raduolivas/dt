import type { ComponentResponse, HealthField, HealthState } from '../api/types';

const LOOK: Record<HealthState, { cls: string; text: string }> = {
  ok: { cls: 'is-ok', text: 'ok' },
  warning: { cls: 'is-warn', text: 'warning' },
  failed: { cls: 'is-fail', text: 'failed' },
  // R22 / R31: Mission Control cannot ask (mTLS-only health); that is not a failure, so it is never red.
  not_probed: { cls: 'is-neutral', text: 'not probed (mTLS)' },
  unknown: { cls: 'is-neutral', text: 'unknown' },
};
const look = (s: HealthState) => LOOK[s] ?? LOOK.unknown;

function Line({ name, field }: { name: string; field: HealthField }) {
  const l = look(field.state);
  return (
    <div className={`mc-health-line ${l.cls}`} data-testid={`health-${name}`}>
      <dt>{name}</dt>
      <dd>
        {l.text}
        {field.reason ? <span className="mc-muted"> · {field.reason}</span> : null}
      </dd>
    </div>
  );
}

/** Liveness, readiness, dependencies and business health, each on its own line (spec 6.4). */
export function HealthTab({ health }: { health: ComponentResponse['health'] }) {
  return (
    <dl className="mc-health">
      <Line name="liveness" field={health.liveness} />
      <Line name="readiness" field={health.readiness} />
      <div className="mc-health-line" data-testid="health-dependencies">
        <dt>dependencies</dt>
        <dd>
          {health.dependencies.length === 0 ? (
            <span className="mc-muted">none seen</span>
          ) : (
            health.dependencies.map((d) => (
              <span key={d.id} className={`mc-dep ${look(d.state).cls}`}>
                {d.id} ({look(d.state).text})
              </span>
            ))
          )}
        </dd>
      </div>
      <Line name="business" field={health.business} />
    </dl>
  );
}
