import type { Insets } from '@dt/renderer';

/**
 * Where the cards go, and which part of the map stays free for the renderer to frame store, courier and home in:
 * - `sheet`: a bottom sheet on phones (portrait), collapsible; it never covers more than `SHEET_MAX` of the height.
 * - `line`: the collapsed sheet on a short landscape screen (under `LINE_MAX_HEIGHT`): one line at the bottom.
 * - `side`: a side panel from `SIDE_MIN_WIDTH`, and the opened sheet on a short landscape screen.
 */
export type LayoutKind = 'sheet' | 'line' | 'side';

export const GUTTER = 12;
export const SIDE_MIN_WIDTH = 900;
export const LINE_MAX_HEIGHT = 500;
export const LINE_HEIGHT = 48;
export const SIDE_WIDTH = 360;
/** Side panel width on a short landscape phone (at most half the width). */
export const SIDE_WIDTH_SMALL = 320;
/** The most of the height the bottom sheet may cover. */
export const SHEET_MAX = 0.72;
/** The collapsed sheet's height until it is measured. */
const SHEET_PEEK = 200;

export interface Rect {
  left: number;
  top: number;
  width: number;
  height: number;
}

export interface LayoutInput {
  /** The SDK's box (CSS px). */
  width: number;
  height: number;
  /** The sheet is opened (phones); the side panel on wide screens is always open. */
  expanded: boolean;
  /** The panel's box inside the SDK's box once the browser laid it out; estimated from the constants until then. */
  panel?: Rect | null;
}

export interface Layout {
  kind: LayoutKind;
  /** What the panel covers, for `Renderer.setInsets`. */
  insets: Insets;
  /** The map left visible. */
  map: Rect;
  /** Width of the side panel (CSS px), when `kind` is `side`. */
  sideWidth: number;
}

export function layoutKind(width: number, height: number, expanded: boolean): LayoutKind {
  if (width >= SIDE_MIN_WIDTH) return 'side';
  if (height < LINE_MAX_HEIGHT && width > height) return expanded ? 'side' : 'line';
  return 'sheet';
}

export function computeLayout({ width, height, expanded, panel }: LayoutInput): Layout {
  const kind = layoutKind(width, height, expanded);
  const measured = panel && panel.width > 0 && panel.height > 0 ? panel : null;
  const insets: Insets = { top: 0, right: 0, bottom: 0, left: 0 };
  const sideWidth = width >= SIDE_MIN_WIDTH ? SIDE_WIDTH : Math.min(SIDE_WIDTH_SMALL, Math.round(width / 2));
  if (kind === 'side') {
    insets.left = Math.min(width / 2, measured ? measured.left + measured.width : GUTTER + sideWidth);
  } else if (kind === 'line') {
    insets.bottom = measured ? height - measured.top : GUTTER + LINE_HEIGHT;
  } else {
    const cover = measured ? height - measured.top : GUTTER + (expanded ? height : SHEET_PEEK);
    insets.bottom = Math.max(0, Math.min(cover, Math.round(height * SHEET_MAX)));
  }
  return {
    kind,
    insets,
    sideWidth,
    map: {
      left: insets.left,
      top: insets.top,
      width: Math.max(0, width - insets.left - insets.right),
      height: Math.max(0, height - insets.top - insets.bottom),
    },
  };
}
