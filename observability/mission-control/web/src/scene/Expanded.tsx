import { store, useStore } from '../state/store';

/** `system › order › <component>`: the order segment only with an order selected; each earlier segment goes back. */
export function Breadcrumb({ label }: { label: (id: string) => string }) {
  const expanded = useStore((s) => s.expanded);
  const orderId = useStore((s) => s.orderId);
  if (expanded === null) return null;
  return (
    <nav className="mc-breadcrumb" aria-label="Breadcrumb">
      <button
        type="button"
        onClick={() => {
          store.expand(null);
          store.selectOrder(null);
        }}
      >
        system
      </button>
      <span aria-hidden="true"> › </span>
      {orderId !== null ? (
        <>
          <button type="button" title={orderId} onClick={() => store.expand(null)}>
            order
          </button>
          <span aria-hidden="true"> › </span>
        </>
      ) : null}
      <span aria-current="page">{label(expanded)}</span>
    </nav>
  );
}
