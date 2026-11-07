import { useEffect } from 'react';
import { openStream } from './api/stream';
import { Cockpit } from './layout/Cockpit';
import { Scene } from './scene/Scene';
import { store, useStore } from './state/store';

/** Routes stream messages into the store. Events and bundles are not accepted while paused. */
export function useLiveStream(): void {
  const orderId = useStore((s) => s.orderId);
  const live = useStore((s) => s.mode === 'live');
  useEffect(() => {
    if (!live) return;
    // Resume from the newest id seen, so the server replays what was missed or marks a gap.
    const lastEventId = store.getState().lastEventId ?? undefined;
    const h = openStream(orderId ?? undefined, (m) => {
      store.noteStreamId(m.id);
      switch (m.type) {
        case 'event':
          store.queueEvents([m.data]);
          break;
        case 'detector':
          store.addFinding(m.data);
          break;
        case 'gap':
          store.addGap(m.data);
          break;
        case 'bundle':
          store.addBundle(m.data, m.id);
          break;
      }
    }, { lastEventId });
    return () => h.close();
  }, [orderId, live]);
}

function Placeholder({ children }: { children: string }) {
  return <p className="mc-placeholder">{children}</p>;
}

export function App() {
  useLiveStream();
  return (
    <Cockpit
      left={<Placeholder>Orders and warnings</Placeholder>}
      center={<Scene />}
      right={<Placeholder>Inspector</Placeholder>}
      bottom={<Placeholder>Timeline</Placeholder>}
    />
  );
}
