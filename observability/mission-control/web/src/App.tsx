import { useEffect } from 'react';
import { openStream } from './api/stream';
import { Cockpit } from './layout/Cockpit';
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
          store.addEvents([m.data]);
          break;
        case 'detector':
          store.addFinding(m.data);
          break;
        case 'gap':
          store.addGap(m.data);
          break;
        case 'bundle':
          store.addBundle(m.data);
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
      center={<Placeholder>3D scene</Placeholder>}
      right={<Placeholder>Inspector</Placeholder>}
      bottom={<Placeholder>Timeline</Placeholder>}
    />
  );
}
