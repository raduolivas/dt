// The prototype's palette. No three.js import here: the tile worker reads these too.

export const COLORS = {
  ground: 0xe4eaf7,
  road: 0xaab5cf,
  roadMark: 0xf4f7ff,
  block: 0xf7f9fe,
  blue: 0x2f5bea,
  blueDark: 0x1e3fb0,
  blueLight: 0x9db4ee,
  white: 0xffffff,
  coral: 0xff6b4a,
  green: 0x37c887,
  leaf: 0x7fcf9f,
  leafDark: 0x58b985,
  trunk: 0x9b7b63,
  skin: 0xf2c3a0,
  shirt: 0xff8a3d,
  pants: 0x2b3350,
  glass: 0xbfd6ff,
} as const;

/** Wall tints of the prototype's generic buildings (multiplied with the window texture). */
export const BUILDING_TINTS = [0xffffff, 0xf1f5ff, 0xe6edff, 0xdde7fb, 0xf6f0ff] as const;

/** Colors of the real-map city per theme (sRGB hex). Day is the prototype's palette extended to real map layers. */
export interface CityColors {
  ground: number;
  /** Top of the raised pale city blocks (everything that is not road, water or park). */
  block: number;
  /** Vertical sides of the blocks and the curbs along roads and quays. */
  blockSide: number;
  road: number;
  /** Footways, cycleways, pedestrian streets: thinner and lighter. */
  roadPath: number;
  roadMark: number;
  water: number;
  park: number;
  walls: readonly number[];
  roof: number;
  roofRim: number;
  roofAccent: number;
  roofAccentRim: number;
  roofUnit: number;
  /** A few land use classes get a faint tint; the rest stays block colored. */
  landuse: Record<string, number>;
}

export const CITY_DAY: CityColors = {
  ground: COLORS.ground,
  block: COLORS.block,
  blockSide: 0xd5ddef,
  road: COLORS.road,
  roadPath: 0xd2daec,
  roadMark: COLORS.roadMark,
  water: 0x9cc3f5,
  park: 0xb2e6c4,
  walls: BUILDING_TINTS,
  roof: 0xe9eef9,
  roofRim: 0xffffff,
  // A soft periwinkle between the prototype's blueLight and block colors: variety on the roofs without competing
  // with the route, the store and the partner accent (which own the strong blue).
  roofAccent: 0xc7d2f0,
  roofAccentRim: 0xe6ecfb,
  roofUnit: 0xcdd6ea,
  landuse: {
    railway: 0xe8ecf6,
    school: 0xf3f0fd,
    university: 0xf3f0fd,
    college: 0xf3f0fd,
    kindergarten: 0xf3f0fd,
    hospital: 0xfbf1f1,
    pitch: 0xc6edd3,
    playground: 0xc6edd3,
  },
};

/** The real-map city by night (albedo under the moon): deep blues, the water darker than the streets. */
export const CITY_NIGHT: CityColors = {
  ground: 0x1a2245,
  block: 0x6c78ad,
  blockSide: 0x4a5590,
  road: 0x3a4474,
  roadPath: 0x4f5a8c,
  roadMark: 0xaab6e6,
  water: 0x1d3a7c,
  park: 0x3f7a74,
  walls: [0xc4cdf0, 0xb8c3ec, 0xb1bde8, 0xa8b5e2, 0xbeb8ea],
  roof: 0x8a96c8,
  roofRim: 0xa9b5e2,
  roofAccent: 0x7d8bc6,
  roofAccentRim: 0x9aa7da,
  roofUnit: 0x76829f,
  landuse: {
    railway: 0x65709f,
    school: 0x7177ad,
    university: 0x7177ad,
    college: 0x7177ad,
    kindergarten: 0x7177ad,
    hospital: 0x7a76a8,
    pitch: 0x467f76,
    playground: 0x467f76,
  },
};

export type ColorKey = keyof typeof COLORS;

/** An emissive glow: `color` null keeps the material's own emissive color (pins in the partner color). */
export interface Glow {
  color: number | null;
  intensity: number;
}

/**
 * What glows: shop and house windows (`glass`, matched by the prototype's glass emissive), vehicle lamps (`lamp`,
 * `tail`), the store's signs, the store and home pins, and the lit windows of the city's buildings.
 */
export type GlowKey = 'glass' | 'lamp' | 'tail' | 'sign' | 'pin' | 'windows';

/**
 * One theme of the scene: the prototype's model colors (`COLORS` keys), the real-map city colors, the sky and the
 * two lights (hemisphere and sun, which is the moon at night), and what glows how much.
 */
export type Palette = Record<ColorKey, number> & {
  sky: number;
  hemiSky: number;
  hemiGround: number;
  hemiIntensity: number;
  sunColor: number;
  sunIntensity: number;
  /** Where the sun (or moon) stands relative to what the camera looks at (scene metres). */
  sunOffset: readonly [number, number, number];
  city: CityColors;
  glow: Record<GlowKey, Glow>;
  /** Strength of the streetlight pools and the courier's headlight (0 = not drawn). */
  streetlights: number;
};

/** The day: the prototype exactly (its `COLORS`, sky, hemisphere and warm sun). */
export const DAY: Palette = {
  ...COLORS,
  sky: COLORS.ground,
  hemiSky: 0xffffff,
  hemiGround: 0xb9c6e6,
  hemiIntensity: 1.5,
  sunColor: 0xffffff,
  sunIntensity: 2.4,
  sunOffset: [-260, 420, 180],
  city: CITY_DAY,
  glow: {
    glass: { color: 0x6f9cff, intensity: 0.25 },
    lamp: { color: 0xfff1b8, intensity: 0.9 },
    tail: { color: COLORS.coral, intensity: 0.5 },
    sign: { color: null, intensity: 0 },
    pin: { color: null, intensity: 0.25 },
    windows: { color: null, intensity: 0 },
  },
  streetlights: 0,
};

/**
 * The night, still the toy city: a deep blue sky, a cool moon (the sun light, moved and recolored), warm lit windows,
 * streetlight pools, the store sign and the pins glowing. The bright accents (blue, coral, green, the rider) keep their
 * day colors so the store, the home and the courier read the same.
 */
export const NIGHT: Palette = {
  ...COLORS,
  ground: CITY_NIGHT.ground,
  road: 0x3a4474,
  roadMark: 0xaab6e6,
  block: 0x6c78ad,
  white: 0xdfe5ff,
  glass: 0x6a7fc0,
  leaf: 0x5fa88f,
  leafDark: 0x468a78,
  trunk: 0x6f5d5a,
  sky: 0x0d1430,
  hemiSky: 0x8796d8,
  hemiGround: 0x1a2147,
  hemiIntensity: 1.0,
  sunColor: 0xc0ccff,
  sunIntensity: 1.0,
  sunOffset: [240, 420, -150],
  city: CITY_NIGHT,
  glow: {
    glass: { color: 0xffc46b, intensity: 1.1 },
    lamp: { color: 0xfff1b8, intensity: 2.2 },
    tail: { color: 0xff5a3a, intensity: 1.6 },
    sign: { color: null, intensity: 0.9 },
    pin: { color: null, intensity: 0.85 },
    windows: { color: null, intensity: 1.35 },
  },
  streetlights: 1,
};
