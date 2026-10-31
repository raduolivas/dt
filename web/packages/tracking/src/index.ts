import { defineTrackingElement } from './element';

export { mount, type MountOptions, type Tracking, type Mode, type Theme } from './mount';
export { DtTrackingElement, defineTrackingElement } from './element';
export { readableOn, ensureContrast } from './branding';
export { pickLang, type Lang } from './i18n';
export type { TileSourceSpec } from '@dt/renderer';

// Loading the SDK (the ES module or the script bundle) registers <dt-tracking>.
defineTrackingElement();
