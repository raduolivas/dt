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
  roofAccent: COLORS.blue,
  roofAccentRim: 0x6f8ff0,
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

/** A first night palette; the full night city (lit windows, streetlights) comes with the theme work. */
export const CITY_NIGHT: CityColors = {
  ground: 0x141b33,
  block: 0x222c4d,
  blockSide: 0x1a2240,
  road: 0x343f66,
  roadPath: 0x2b3556,
  roadMark: 0x9aa9dc,
  water: 0x17305e,
  park: 0x1f4a44,
  walls: [0x4b5a8a, 0x45527f, 0x515f90, 0x3f4b78, 0x55588a],
  roof: 0x3a4673,
  roofRim: 0x5a6aa0,
  roofAccent: 0x2f4fc0,
  roofAccentRim: 0x5a78e0,
  roofUnit: 0x34406a,
  landuse: { railway: 0x262f50, pitch: 0x214a40, playground: 0x214a40 },
};
