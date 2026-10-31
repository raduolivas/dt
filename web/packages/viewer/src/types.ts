import type { Branding, TrackingSnapshot } from '@dt/contract';

export type ViewerState =
  | { kind: 'connecting' }
  | { kind: 'warming' }
  | { kind: 'live'; snapshot: TrackingSnapshot; branding: Branding | null }
  | { kind: 'inactive'; last: TrackingSnapshot | null };

export interface ViewerError {
  code: string;
  detail?: string;
}

export interface ViewerClient {
  start(): void;
  stop(): void;
  onState(cb: (s: ViewerState) => void): () => void;
  onError(cb: (e: ViewerError) => void): () => void;
}

export interface ViewerOptions {
  /** Base URL of the API including the version prefix, for example `https://api.example.com/v1`. */
  api: string;
  /** The link token. Kept in memory only. */
  token: string;
  fetch?: typeof fetch;
  WebSocket?: typeof WebSocket;
  now?: () => number;
}
