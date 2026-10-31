import type { DeliveryStatus, EtaWindow } from '@dt/contract';
import type { Lang } from './i18n';
import { createTracking, type Mode, type Theme, type Tracking } from './mount';

const MODES: readonly string[] = ['3d', '2d'];
const THEMES: readonly string[] = ['light', 'dark', 'auto'];
const LANGS: readonly string[] = ['en', 'nl'];

// Outside a browser (server rendering) there is no HTMLElement to extend; the element is only defined in one.
const Base = (typeof HTMLElement === 'undefined' ? class {} : HTMLElement) as typeof HTMLElement;

/**
 * `<dt-tracking token="…" mode="3d|2d" theme="light|dark|auto" lang="en|nl">`: the tracking view as an element.
 * It dispatches `ready`, `statuschange` (`{ status }`), `etachange` (`{ seconds, window }`), `delivered` and
 * `error` (`{ code }`) as `CustomEvent`s. Changing `mode` or `theme` applies at once; `token` or `lang` remounts.
 */
export class DtTrackingElement extends Base {
  static readonly observedAttributes = ['token', 'mode', 'theme', 'lang'];
  #tracking: Tracking | null = null;

  constructor() {
    super();
    this.attachShadow({ mode: 'open' });
  }

  connectedCallback(): void {
    this.#start();
  }

  disconnectedCallback(): void {
    this.destroy();
  }

  attributeChangedCallback(name: string, old: string | null, value: string | null): void {
    if (old === value || !this.isConnected) return;
    if (name === 'mode' && value && MODES.includes(value)) this.#tracking?.setMode(value as Mode);
    else if (name === 'theme') this.#tracking?.setTheme(value && THEMES.includes(value) ? (value as Theme) : 'auto');
    else if (name === 'token' || name === 'lang') {
      this.destroy();
      this.#start();
    }
  }

  setMode(m: Mode): void {
    this.#tracking?.setMode(m);
  }

  setTheme(t: Theme): void {
    this.#tracking?.setTheme(t);
  }

  /** Releases the map, the stream and every listener; the element shows nothing until it is connected again. */
  destroy(): void {
    this.#tracking?.destroy();
    this.#tracking = null;
  }

  #attr<T extends string>(name: string, allowed: readonly string[]): T | undefined {
    const v = this.getAttribute(name);
    return v !== null && allowed.includes(v) ? (v as T) : undefined;
  }

  #emit(type: string, detail: unknown = null): void {
    this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
  }

  #start(): void {
    const token = this.getAttribute('token');
    if (this.#tracking || !token) return;
    this.#tracking = createTracking(this.shadowRoot!, {
      token,
      mode: this.#attr<Mode>('mode', MODES),
      theme: this.#attr<Theme>('theme', THEMES),
      lang: this.#attr<Lang>('lang', LANGS),
      onReady: () => this.#emit('ready'),
      onStatusChange: (status: DeliveryStatus) => this.#emit('statuschange', { status }),
      onEtaChange: (e: { seconds: number | null; window: EtaWindow }) => this.#emit('etachange', e),
      onDelivered: () => this.#emit('delivered'),
      onError: (e: { code: string }) => this.#emit('error', e),
    });
  }
}

/** Registers `<dt-tracking>` once (a second call, or a second copy of the SDK on the page, is a no-op). */
export function defineTrackingElement(): void {
  if (typeof customElements === 'undefined' || customElements.get('dt-tracking')) return;
  customElements.define('dt-tracking', DtTrackingElement);
}
