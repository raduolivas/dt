import type { DeliveryStatus, Vehicle } from '@dt/contract';

export type Lang = 'en' | 'nl';

// The customer copy, one table per language with the same keys. The English strings marked "exact" are fixed by the
// M4 plan; the Dutch table is a draft for the owner's review (ruling R20: friendly Dutch with "je").
const en = {
  // exact
  updatingLocation: 'Updating courier location…',
  locatingCourier: 'Locating courier…',
  calculating: 'Calculating delivery time…',
  estimated: 'estimated',
  yourAddress: 'Your address',
  inactive: 'This tracking link is no longer active',
  warming: 'Getting your delivery ready…',

  loading: 'Loading…',
  region: 'Delivery tracking',
  arrivesBetween: 'Arrives between',
  arrivesIn: 'Arrives in',
  minutes: '{n} min',

  head_confirmed: 'Order confirmed',
  head_courier_assigned: 'Courier assigned',
  head_at_pickup: 'Courier at the store',
  head_picked_up: 'On the way',
  head_arriving: 'Almost there',
  sub_confirmed: '{store} is preparing your order',
  sub_courier_assigned: '{store} is preparing your order',
  sub_at_pickup: 'Your order is almost ready to go',
  sub_picked_up: 'Your order is on its way to you',
  sub_arriving: 'Your courier is almost at your door',

  tl_confirmed: 'Order confirmed',
  tl_courier_assigned: 'Courier assigned',
  tl_at_pickup: 'Courier at the store',
  tl_picked_up: 'Order picked up',
  tl_arriving: 'Almost there',
  tl_delivered: 'Delivered',
  tl_cancelled: 'Cancelled',
  tl_failed: 'Not delivered',
  progress: 'Delivery progress',

  final_delivered: 'Delivered',
  final_delivered_at: 'Delivered at {time}. Enjoy!',
  final_delivered_sub: 'Your order has been delivered. Enjoy!',
  final_cancelled: 'Order cancelled',
  final_not_completed: 'Delivery not completed',
  final_contact: 'Questions? Contact {store}.',

  courier: 'Your courier',
  v_bicycle: 'By bike',
  v_e_bike: 'By e-bike',
  v_scooter: 'By scooter',
  v_car: 'By car',
  v_on_foot: 'On foot',

  pickup: 'Pickup',
  dropoff: 'Delivery',

  show2d: 'Show the map in 2D',
  show3d: 'Show the map in 3D',
  showDetails: 'Show details',
  hideDetails: 'Hide details',
};

export type CopyKey = keyof typeof en;

const nl: Record<CopyKey, string> = {
  updatingLocation: 'Locatie van je bezorger wordt bijgewerkt…',
  locatingCourier: 'Locatie van je bezorger ophalen…',
  calculating: 'Bezorgtijd berekenen…',
  estimated: 'geschat',
  yourAddress: 'Je adres',
  inactive: 'Deze trackinglink is niet meer actief',
  warming: 'Je bezorging wordt klaargezet…',

  loading: 'Laden…',
  region: 'Je bezorging volgen',
  arrivesBetween: 'Verwacht tussen',
  arrivesIn: 'Bij je over',
  minutes: '{n} min',

  head_confirmed: 'Bestelling bevestigd',
  head_courier_assigned: 'Bezorger gevonden',
  head_at_pickup: 'Bezorger is bij de winkel',
  head_picked_up: 'Onderweg naar je',
  head_arriving: 'Bijna bij je',
  sub_confirmed: '{store} maakt je bestelling klaar',
  sub_courier_assigned: '{store} maakt je bestelling klaar',
  sub_at_pickup: 'Je bestelling is bijna klaar voor vertrek',
  sub_picked_up: 'Je bestelling is onderweg naar je',
  sub_arriving: 'Je bezorger is bijna bij je deur',

  tl_confirmed: 'Bestelling bevestigd',
  tl_courier_assigned: 'Bezorger gevonden',
  tl_at_pickup: 'Bezorger bij de winkel',
  tl_picked_up: 'Bestelling opgehaald',
  tl_arriving: 'Bijna bij je',
  tl_delivered: 'Bezorgd',
  tl_cancelled: 'Geannuleerd',
  tl_failed: 'Niet bezorgd',
  progress: 'Voortgang van je bezorging',

  final_delivered: 'Bezorgd',
  final_delivered_at: 'Bezorgd om {time}. Geniet ervan!',
  final_delivered_sub: 'Je bestelling is bezorgd. Geniet ervan!',
  final_cancelled: 'Bestelling geannuleerd',
  final_not_completed: 'Bezorging niet gelukt',
  final_contact: 'Vragen? Neem contact op met {store}.',

  courier: 'Je bezorger',
  v_bicycle: 'Op de fiets',
  v_e_bike: 'Op de e-bike',
  v_scooter: 'Op de scooter',
  v_car: 'Met de auto',
  v_on_foot: 'Te voet',

  pickup: 'Ophalen',
  dropoff: 'Bezorgen',

  show2d: 'Toon de kaart in 2D',
  show3d: 'Toon de kaart in 3D',
  showDetails: 'Details tonen',
  hideDetails: 'Details verbergen',
};

export const COPY: Record<Lang, Record<CopyKey, string>> = { en, nl };

/** The copy for `key`, with `{name}` placeholders filled from `vars`. */
export function t(key: CopyKey, lang: Lang, vars: Record<string, string | number> = {}): string {
  return COPY[lang][key].replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

/** `nl` when any browser language is Dutch (`nl`, `nl-NL`, `nl-BE`, …), else `en`. */
export function pickLang(languages: readonly string[]): Lang {
  return languages.some((l) => /^nl(-|$)/i.test(l)) ? 'nl' : 'en';
}

export const timelineKey = (s: DeliveryStatus): CopyKey => `tl_${s}` as CopyKey;
export const vehicleKey = (v: Vehicle): CopyKey => `v_${v}` as CopyKey;
