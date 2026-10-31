import { createCourier } from '@dt/courier';
import { pickLang } from './i18n';
import { mountCourierPage } from './view';
import { createWakeLock } from './wakelock';
import './page.css';

// The hosted courier page (courier.<domain>/s/<code>, or / to resume). The API base is baked in at build time
// (VITE_DT_API; without it, the local stack), like tracking-page's default. The link code stays in memory: the SDK
// exchanges it once and the view then replaces the address with `/`.
type BuildEnv = Record<string, string | undefined>;
const api = (import.meta.env as BuildEnv).VITE_DT_API || 'http://localhost:8080/v1';

const lang = pickLang(navigator.languages?.length ? navigator.languages : [navigator.language]);
document.documentElement.lang = lang;

mountCourierPage(document.getElementById('root') ?? document.body, {
  api,
  path: location.pathname,
  lang,
  makeCourier: createCourier,
  replaceUrl: (path) => history.replaceState(null, '', path),
  wakeLock: createWakeLock(),
});
