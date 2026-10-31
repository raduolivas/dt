---
title: Tracking page and SDK
description: Send customers to the hosted tracking page, embed it in an iframe, or add the DeliveryOnTrack tracking view to your own page with the SDK.
---

# Tracking page and SDK

DeliveryOnTrack shows your customer a live 3D map of the delivery. You can use it in three ways. Pick the one that
fits your app:

| Way | You write | The customer sees |
|---|---|---|
| Hosted page | nothing, only send the link | a full page on `track.<domain>` |
| Iframe | one `<iframe>` tag and a setting | the hosted page inside your page |
| SDK | a script tag and one element | the tracking view inside your page, as part of it |

All three use the tracking link from [Tracking links](tracking-links.md). The page shows the courier only when the
delivery model allows it.

> The hosted page and the SDK are on staging only for now. Staging hosts:
> `track.staging.deliveryontrack.io`, `cdn.staging.deliveryontrack.io`, `api.staging.deliveryontrack.io`. Your
> production hosts will be given to you when production opens.

## The hosted page

The link in `tracking.url` is the hosted page:

```
https://track.staging.deliveryontrack.io/t/Zr7wT2nKd9sH4yJ6uB3xQa
```

Open it in a browser tab, an in-app webview or a link in an SMS or email. It works on phones and desktops, in
light and dark mode, and it needs no login and no cookies.

You can also redirect the customer to it. Your own page, for example `https://shop.example.com/orders/123/track`,
answers with an HTTP redirect to `tracking.url` (status 302). Keep the token on your server side until you
redirect: it is a credential for that one delivery.

An unknown, rotated, revoked or expired link shows a calm card: "This tracking link is no longer active". The
page does not tell the visitor whether the link ever existed. A delivery that has already ended shows its final
summary instead, when the page saw it.

If DeliveryOnTrack is briefly unavailable, for example in the first seconds after you create the delivery or
during an outage, the page shows "Getting your delivery ready…" and keeps trying in the background until the
tracking is back. An outage never shows the inactive card: that card means the link itself is no longer valid.

## Embed the hosted page in an iframe

By default nobody may put the hosted page in an iframe. You allow your own sites with the tenant setting
`embed_origins`.

```bash
dtctl tenant settings ... \
  --embed-origin https://shop.example.com \
  --embed-origin https://*.example.com
```

`dtctl tenant settings` replaces the settings as a whole. Pass every setting you want to keep, including the
branding flags. Until the console arrives (M7), DeliveryOnTrack staff set this for you.

The rules for each origin:

- It must start with `https://`. An `http://` origin is refused.
- It is a host with an optional port: `https://shop.example.com` or `https://shop.example.com:8443`.
- No path, no query and no fragment. `https://shop.example.com/app` is refused.
- The only wildcard is a leading `*.`, for subdomains: `https://*.example.com`. It needs a real domain, so
  `https://*.com` is refused.
- At most 20 origins, and no duplicates.
- An empty list means that nobody may embed the page.

Then use the link as the iframe source:

```html
<iframe
  src="https://track.staging.deliveryontrack.io/t/Zr7wT2nKd9sH4yJ6uB3xQa"
  title="Delivery tracking"
  style="width:100%;height:600px;border:0"
  allow="">
</iframe>
```

The browser enforces the rule with `frame-ancestors`. A page on an origin that is not in your list sees a blank
frame. If your iframe is blank, check the origin first.

`embed_origins` only limits iframes. The SDK below works on any origin that holds a valid link token.

## Add the tracking view with the SDK

The SDK draws the same view as the hosted page inside your page. It uses a Shadow DOM, so your CSS cannot break it
and it cannot break your CSS.

Load it from the CDN. The path has the version of the SDK in it. On staging the version is a git commit, which
DeliveryOnTrack gives you:

```
https://cdn.staging.deliveryontrack.io/sdk/tracking/<version>/tracking.js         (ES module)
https://cdn.staging.deliveryontrack.io/sdk/tracking/<version>/tracking.iife.js    (plain script)
```

Loading either file registers the `<dt-tracking>` element.

### The element

```html
<script src="https://cdn.staging.deliveryontrack.io/sdk/tracking/<version>/tracking.iife.js"></script>

<dt-tracking
  token="Zr7wT2nKd9sH4yJ6uB3xQa"
  mode="3d"
  theme="auto"
  lang="nl"
  style="display:block;width:100%;height:600px">
</dt-tracking>
```

Give the element a size. The view fills it.

Attributes:

| Attribute | Values | Default | When you change it |
|---|---|---|---|
| `token` | the link token (the part after `/t/`) | none: nothing is shown | the view restarts |
| `mode` | `3d`, `2d` | `3d` | applies at once |
| `theme` | `light`, `dark`, `auto` | your branding theme, else `auto` (follows the device) | applies at once |
| `lang` | `en`, `nl` | from the browser languages | the view restarts |

An unknown value is ignored. Methods: `setMode('3d' | '2d')`, `setTheme('light' | 'dark' | 'auto')` and
`destroy()`.

Events (all are `CustomEvent`s and bubble):

| Event | `detail` | When |
|---|---|---|
| `ready` | none | the first snapshot (or the inactive card) is shown |
| `statuschange` | `{ status }` | the delivery status changed |
| `etachange` | `{ seconds, window }` | the ETA changed (`seconds` can be `null`) |
| `delivered` | none | the delivery is delivered |
| `error` | `{ code }` | something went wrong; the customer only sees a calm card |

```js
const el = document.querySelector('dt-tracking');
el.addEventListener('statuschange', (e) => console.log(e.detail.status));
el.addEventListener('delivered', () => showThankYou());
el.addEventListener('error', (e) => console.warn(e.detail.code));
```

### `mount()`

If you do not use custom elements, call `mount`. It is exported by the ES module:

```html
<div id="track" style="width:100%;height:600px"></div>
<script type="module">
  import { mount } from 'https://cdn.staging.deliveryontrack.io/sdk/tracking/<version>/tracking.js';

  const tracking = mount(document.getElementById('track'), {
    token: 'Zr7wT2nKd9sH4yJ6uB3xQa',
    mode: '3d',
    theme: 'auto',
    lang: 'en',
    onReady: () => {},
    onStatusChange: (status) => {},
    onEtaChange: ({ seconds, window }) => {},
    onDelivered: () => {},
    onError: ({ code }) => {},
  });

  // later
  tracking.setMode('2d');
  tracking.setTheme('dark');
  tracking.destroy();
</script>
```

Options:

| Option | Meaning |
|---|---|
| `token` | the link token. It is kept in memory only. Required |
| `mode` | `'3d'` (default) or `'2d'` |
| `theme` | `'light'`, `'dark'` or `'auto'`. Unset: the branding theme, else `'auto'` |
| `lang` | `'en'` or `'nl'`. Unset: from the browser |
| `api` | the API base URL with `/v1`. The default is the one the SDK build was made for. Leave it unset |
| `tiles` | the map tile source. The default is the one the SDK build was made for. Leave it unset |
| `onReady`, `onStatusChange`, `onEtaChange`, `onDelivered`, `onError` | the same events as above, as callbacks |

`mount` returns `{ setMode, setTheme, destroy }`. Call `destroy()` when you remove the view: it stops the stream
and frees the map.

## What your page must allow (CSP)

If your page sends a `Content-Security-Policy` header, the SDK needs these origins. Staging values are shown;
replace them with your production hosts later.

| Directive | Add | Why |
|---|---|---|
| `script-src` | `https://cdn.staging.deliveryontrack.io` | the SDK file |
| `connect-src` | `https://api.staging.deliveryontrack.io` and `wss://api.staging.deliveryontrack.io` | the session, snapshots, and the live stream (WebSocket, or SSE as a fallback) |
| `connect-src` | `https://cdn.staging.deliveryontrack.io` | the map tiles (range requests to one file) |
| `img-src` | `https://track.staging.deliveryontrack.io` | your logo, if you set one (it is served from the tracking host) |
| `img-src` | `data: blob:` | images the 3D view makes itself |
| `worker-src` | `blob:` | the geometry worker, which is bundled inside the SDK |
| `style-src` | `'unsafe-inline'` | the SDK writes its styles at run time into its Shadow DOM |

Example:

```
Content-Security-Policy:
  script-src 'self' https://cdn.staging.deliveryontrack.io;
  connect-src 'self' https://api.staging.deliveryontrack.io wss://api.staging.deliveryontrack.io
              https://cdn.staging.deliveryontrack.io;
  img-src 'self' data: blob: https://track.staging.deliveryontrack.io;
  worker-src blob:;
  style-src 'self' 'unsafe-inline'
```

Notes:

- `https://` in a CSP source does not cover `wss://`. Add both for the API.
- The map is drawn with WebGL on a canvas. This needs no CSP entry. If WebGL is not available, the view falls
  back to a 2D map.
- The hosted page and the iframe need none of this: the hosted page sends its own policy.

## Branding

The view uses your branding when you have set it: a display name, a logo, a primary color and a theme. All are
optional. Set them with `dtctl tenant settings` (the console will do this in M7):

| Setting | Flag | Rule |
|---|---|---|
| Display name | `--brand-name` | up to 60 characters |
| Primary color | `--brand-color` | `#RRGGBB` |
| Theme | `--brand-theme` | `light`, `dark` or `auto` |
| Logo | `--brand-logo` | PNG or WebP, up to 100 KB and 512 x 512 pixels |

The name and logo show in a card at the top of the details. The primary color is used for accents on the cards. If
the color has too little contrast on the card, the view uses a slightly darker shade (or lighter, at night) so the
text stays readable. The theme is the default when you do not set `theme` yourself. Branding applies to the hosted
page, the iframe and the SDK.

## Languages

The view speaks English (`en`) and Dutch (`nl`). Everything the customer reads is translated, including the labels
on the 3D map ("Your address" is "Je adres").

- Hosted page: the language follows the browser. Any Dutch locale (`nl`, `nl-NL`, `nl-BE`) gives Dutch. Anything
  else gives English.
- SDK: the same, unless you set `lang`.
