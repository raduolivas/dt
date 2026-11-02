export type Lang = 'en' | 'nl';

// The courier page copy, one table per language with the same keys. The Dutch table is a draft for the owner's review
// (friendly Dutch with "je", like the tracking page).
const en = {
  appName: 'Courier',
  loading: 'Loading…',

  // consent
  consentTitle: 'Start your shift',
  consentIntro: 'Your dispatcher sent you this link to share your location while you deliver.',
  consentWhat: 'What we collect',
  consentWhatBody: 'Your phone’s location, only while you have a delivery assigned and only while this page is open.',
  consentWhy: 'Why',
  consentWhyBody: 'So your dispatcher can follow your deliveries and customers can see you coming.',
  consentWho: 'Who sees it',
  consentWhoBody: 'Your dispatcher, and the customer during their own delivery.',
  startShift: 'Start shift',
  starting: 'Starting your shift…',

  // errors and end screens
  askNewLink: 'Ask your dispatcher for a new link.',
  endedTitle: 'Your shift has ended',
  endedBody: 'This shift is over, so your location is no longer shared. Ask your dispatcher for a new link.',
  expiredTitle: 'Your shift has expired',
  expiredBody: 'This shift ran out of time, so your location is no longer shared. Ask your dispatcher for a new link.',
  usedTitle: 'This link was already used',
  usedBody: 'Each link works once. Ask your dispatcher for a new link.',
  invalidTitle: 'This link doesn’t work',
  invalidBody: 'It may have expired or be mistyped. Ask your dispatcher for a new link.',
  noSessionTitle: 'No shift found',
  noSessionBody: 'There is no active shift on this phone. Ask your dispatcher for a new link.',
  doneTitle: 'Shift ended',
  doneBody: 'Thanks! Your location is no longer shared.',
  retryTitle: 'Can’t connect right now',
  retryBody: 'Check your connection and try again.',
  retry: 'Try again',

  // location denied help
  deniedTitle: 'Location is blocked',
  deniedBody: 'Your dispatcher can’t see your position. You can still update your deliveries. To turn location on:',
  deniedStep1: 'Open your phone’s Settings and find your browser (for example Safari).',
  deniedStep2: 'Open Location and choose “Ask” or “While using”.',
  deniedStep3: 'Come back here and reload this page.',
  gpsAsking: 'Allow location when your phone asks.',

  // status bar
  gpsOn: 'Location on',
  gpsOff: 'Location off',
  gpsAskingShort: 'Asking for location',
  gpsDenied: 'Location blocked',
  online: 'Online',
  offline: 'Offline',
  updatesWaiting: '{n} updates waiting',
  tapsUnsent: '{n} not sent yet',

  // deliveries
  yourDeliveries: 'Your deliveries',
  noDeliveries: 'No deliveries right now. New ones appear here by themselves.',
  from: 'Pick up at',
  to: 'Deliver to',
  notes: 'Notes',
  items: 'Items',
  st_confirmed: 'Being prepared',
  st_courier_assigned: 'Assigned to you',
  st_at_pickup: 'You are at the store',
  st_picked_up: 'Picked up, on your way',
  st_arriving: 'Almost there',
  st_delivered: 'Delivered',
  st_cancelled: 'Cancelled',
  st_failed: 'Not delivered',
  act_at_pickup: 'I’m at the store',
  act_pickup_confirmed: 'Picked up',
  act_delivered: 'Delivered',
  act_arriving: 'Almost there',
  act_failed: 'Can’t deliver',
  notSentYet: 'Not sent yet. It will go out as soon as you are online.',
  waitStore: 'The store confirms pickup',
  waitBoth: 'Waiting for the store to confirm',
  noLongerYours: 'This delivery is no longer yours',
  notSent: 'Not sent: {action}. Check with your dispatcher.',
  alreadyUpdated: 'This delivery was already updated. It is now: {status}.',
  dismiss: 'OK',

  // can't deliver
  whyTitle: 'Why can’t you deliver?',
  reason_customer_unreachable: 'Customer not reachable',
  reason_address_not_found: 'Can’t find the address',
  reason_refused: 'Customer refused it',
  reason_damaged: 'Order is damaged',
  reason_other: 'Other reason',
  otherLabel: 'What happened?',
  send: 'Send',
  cancel: 'Back',

  // end shift
  keepScreenOn: 'Keep this screen on while you deliver, or your location may stop.',
  endShift: 'End shift',
  endConfirmTitle: 'End your shift?',
  endConfirmBody: 'Your location stops being shared and you won’t get new deliveries on this link.',
  endConfirmUnsent: '{n} not sent yet. Stay online a moment so they go out.',
  endConfirmYes: 'Yes, end shift',
  endConfirmNo: 'Keep going',
};

const nl: typeof en = {
  appName: 'Koerier',
  loading: 'Laden…',

  consentTitle: 'Start je dienst',
  consentIntro: 'Je planner stuurde je deze link om je locatie te delen terwijl je bezorgt.',
  consentWhat: 'Wat we verzamelen',
  consentWhatBody: 'De locatie van je telefoon, alleen zolang je een bezorging toegewezen hebt en deze pagina openstaat.',
  consentWhy: 'Waarom',
  consentWhyBody: 'Zodat je planner je bezorgingen kan volgen en klanten kunnen zien dat je eraan komt.',
  consentWho: 'Wie het ziet',
  consentWhoBody: 'Je planner, en de klant tijdens zijn of haar eigen bezorging.',
  startShift: 'Start dienst',
  starting: 'Je dienst wordt gestart…',

  askNewLink: 'Vraag je planner om een nieuwe link.',
  endedTitle: 'Je dienst is afgelopen',
  endedBody: 'Deze dienst is voorbij, dus je locatie wordt niet meer gedeeld. Vraag je planner om een nieuwe link.',
  expiredTitle: 'Je dienst is verlopen',
  expiredBody: 'De tijd van deze dienst is om, dus je locatie wordt niet meer gedeeld. Vraag je planner om een nieuwe link.',
  usedTitle: 'Deze link is al gebruikt',
  usedBody: 'Elke link werkt maar één keer. Vraag je planner om een nieuwe link.',
  invalidTitle: 'Deze link werkt niet',
  invalidBody: 'Hij is misschien verlopen of verkeerd overgetypt. Vraag je planner om een nieuwe link.',
  noSessionTitle: 'Geen dienst gevonden',
  noSessionBody: 'Er is geen actieve dienst op deze telefoon. Vraag je planner om een nieuwe link.',
  doneTitle: 'Dienst afgerond',
  doneBody: 'Bedankt! Je locatie wordt niet meer gedeeld.',
  retryTitle: 'Geen verbinding op dit moment',
  retryBody: 'Controleer je verbinding en probeer het opnieuw.',
  retry: 'Opnieuw proberen',

  deniedTitle: 'Locatie is geblokkeerd',
  deniedBody: 'Je planner ziet je positie niet. Je kunt je bezorgingen nog wel bijwerken. Zo zet je locatie aan:',
  deniedStep1: 'Open de Instellingen van je telefoon en zoek je browser (bijvoorbeeld Safari).',
  deniedStep2: 'Open Locatie en kies “Vraag” of “Tijdens gebruik”.',
  deniedStep3: 'Kom terug en laad deze pagina opnieuw.',
  gpsAsking: 'Sta locatie toe als je telefoon erom vraagt.',

  gpsOn: 'Locatie aan',
  gpsOff: 'Locatie uit',
  gpsAskingShort: 'Vraagt om locatie',
  gpsDenied: 'Locatie geblokkeerd',
  online: 'Online',
  offline: 'Offline',
  updatesWaiting: '{n} updates wachten',
  tapsUnsent: '{n} nog niet verstuurd',

  yourDeliveries: 'Je bezorgingen',
  noDeliveries: 'Nu geen bezorgingen. Nieuwe verschijnen hier vanzelf.',
  from: 'Ophalen bij',
  to: 'Bezorgen bij',
  notes: 'Opmerkingen',
  items: 'Bestelling',
  st_confirmed: 'Wordt voorbereid',
  st_courier_assigned: 'Aan jou toegewezen',
  st_at_pickup: 'Je bent bij de winkel',
  st_picked_up: 'Opgehaald, onderweg',
  st_arriving: 'Bijna daar',
  st_delivered: 'Bezorgd',
  st_cancelled: 'Geannuleerd',
  st_failed: 'Niet bezorgd',
  act_at_pickup: 'Ik ben bij de winkel',
  act_pickup_confirmed: 'Opgehaald',
  act_delivered: 'Bezorgd',
  act_arriving: 'Bijna daar',
  act_failed: 'Kan niet bezorgen',
  notSentYet: 'Nog niet verstuurd. Het gaat weg zodra je online bent.',
  waitStore: 'De winkel bevestigt het ophalen',
  waitBoth: 'Wachten op bevestiging van de winkel',
  noLongerYours: 'Deze bezorging is niet meer van jou',
  notSent: 'Niet verstuurd: {action}. Vraag het na bij je planner.',
  alreadyUpdated: 'Deze bezorging was al bijgewerkt. De status is nu: {status}.',
  dismiss: 'OK',

  whyTitle: 'Waarom kun je niet bezorgen?',
  reason_customer_unreachable: 'Klant niet bereikbaar',
  reason_address_not_found: 'Adres niet te vinden',
  reason_refused: 'Klant weigert de bestelling',
  reason_damaged: 'Bestelling is beschadigd',
  reason_other: 'Andere reden',
  otherLabel: 'Wat is er gebeurd?',
  send: 'Versturen',
  cancel: 'Terug',

  keepScreenOn: 'Houd dit scherm aan terwijl je bezorgt, anders stopt je locatie mogelijk.',
  endShift: 'Dienst beëindigen',
  endConfirmTitle: 'Je dienst beëindigen?',
  endConfirmBody: 'Je locatie wordt niet meer gedeeld en je krijgt geen nieuwe bezorgingen via deze link.',
  endConfirmUnsent: '{n} nog niet verstuurd. Blijf even online zodat ze nog weggaan.',
  endConfirmYes: 'Ja, beëindig dienst',
  endConfirmNo: 'Doorgaan',
};

export type CopyKey = keyof typeof en;
export const COPY: Record<Lang, Record<CopyKey, string>> = { en, nl };

/** The copy for `key`, with `{name}` placeholders filled from `vars`. */
export function t(key: CopyKey, lang: Lang, vars: Record<string, string | number> = {}): string {
  return COPY[lang][key].replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

/** `nl` when any browser language is Dutch (`nl`, `nl-NL`, `nl-BE`, …), else `en`. */
export function pickLang(languages: readonly string[]): Lang {
  return languages.some((l) => /^nl(-|$)/i.test(l)) ? 'nl' : 'en';
}

/** The fixed "can't deliver" reasons, in the order shown; `other` takes free text. */
export const FAIL_REASONS = ['customer_unreachable', 'address_not_found', 'refused', 'damaged'] as const;
