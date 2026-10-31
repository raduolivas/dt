import { describe, expect, it } from 'vitest';
import { COPY, pickLang, t } from './i18n';

describe('pickLang', () => {
  it('picks nl for any Dutch browser language and en otherwise', () => {
    expect(pickLang(['nl-NL'])).toBe('nl');
    expect(pickLang(['nl'])).toBe('nl');
    expect(pickLang(['de-DE', 'nl-BE'])).toBe('nl');
    expect(pickLang(['de'])).toBe('en');
    expect(pickLang(['en-US'])).toBe('en');
    expect(pickLang([])).toBe('en');
  });
});

describe('copy tables', () => {
  it('has every key in both languages, none empty', () => {
    const en = Object.keys(COPY.en).sort();
    const nl = Object.keys(COPY.nl).sort();
    expect(nl).toEqual(en);
    for (const k of en) {
      expect(COPY.en[k as keyof typeof COPY.en].trim()).not.toBe('');
      expect(COPY.nl[k as keyof typeof COPY.nl].trim()).not.toBe('');
    }
  });

  it('keeps the exact English customer copy', () => {
    expect(t('updatingLocation', 'en')).toBe('Updating courier location…');
    expect(t('locatingCourier', 'en')).toBe('Locating courier…');
    expect(t('calculating', 'en')).toBe('Calculating delivery time…');
    expect(t('estimated', 'en')).toBe('estimated');
    expect(t('yourAddress', 'en')).toBe('Your address');
    expect(t('inactive', 'en')).toBe('This tracking link is no longer active');
    expect(t('warming', 'en')).toBe('Getting your delivery ready…');
  });

  it('fills placeholders and uses the same placeholders in both languages', () => {
    expect(t('minutes', 'en', { n: 12 })).toBe('12 min');
    for (const k of Object.keys(COPY.en) as (keyof typeof COPY.en)[]) {
      const vars = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
      expect(vars(COPY.nl[k]), k).toEqual(vars(COPY.en[k]));
    }
  });
});
