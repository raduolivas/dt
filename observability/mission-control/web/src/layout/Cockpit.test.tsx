import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { store } from '../state/store';
import { Cockpit } from './Cockpit';

beforeEach(() => store.reset());

function ui() {
  return render(
    <Cockpit left={<p>order-panel</p>} center={<p>scene-slot</p>} right={<p>inspector-slot</p>} bottom={<p>timeline-slot</p>} />,
  );
}

describe('Cockpit', () => {
  it('cockpitCollapsesPanels', () => {
    ui();
    expect(screen.getByText('order-panel')).toBeInTheDocument();
    const left = screen.getByRole('button', { name: /collapse left panel/i });
    expect(left).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(left);
    expect(screen.queryByText('order-panel')).not.toBeInTheDocument();
    expect(screen.getByText('scene-slot')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /expand left panel/i })).toHaveAttribute('aria-expanded', 'false');

    fireEvent.click(screen.getByRole('button', { name: /collapse right panel/i }));
    expect(screen.queryByText('inspector-slot')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /collapse timeline/i }));
    expect(screen.queryByText('timeline-slot')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /expand left panel/i }));
    expect(screen.getByText('order-panel')).toBeInTheDocument();
  });

  it('full scene toggle collapses every panel and restores them', () => {
    ui();
    fireEvent.click(screen.getByRole('button', { name: /full scene/i }));
    expect(screen.queryByText('order-panel')).not.toBeInTheDocument();
    expect(screen.queryByText('inspector-slot')).not.toBeInTheDocument();
    expect(screen.queryByText('timeline-slot')).not.toBeInTheDocument();
    expect(screen.getByText('scene-slot')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /full scene/i }));
    expect(screen.getByText('order-panel')).toBeInTheDocument();
  });

  it('top bar switches mode and shows the warning count', () => {
    store.addFinding({ id: 1, kind: 'detector' } as never);
    ui();
    expect(screen.getByText(/1 warning/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    expect(store.getState().mode).toBe('paused');
    fireEvent.click(screen.getByRole('button', { name: 'Live' }));
    expect(store.getState().mode).toBe('live');
  });

  it('shows a SIMULATED badge when the buffer holds simulated events', () => {
    store.addEvents([{ id: 1, kind: 'span', origin: 'simulated', occurredAt: new Date().toISOString() } as never]);
    ui();
    expect(screen.getByText('SIMULATED')).toBeInTheDocument();
  });
});
