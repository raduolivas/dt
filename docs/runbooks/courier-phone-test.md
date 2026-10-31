# Runbook: the courier page on a phone, tracking page on a laptop (owner, M5 checkpoint)

Result: the courier page runs on your iPhone over the home Wi-Fi and reports real GPS to the local stack, while the tracking page on the laptop shows the courier moving. Needs a laptop on the same network as the phone, running the local stack.

iOS only gives a page geolocation in a secure context, and an https page may only call an https API. So the phone uses `https://<LAN-IP>:8443` (page) and `https://<LAN-IP>:8444` (courier API only), signed by a small local CA that you trust on the phone once.

## 1. Start the stack and the HTTPS side

```sh
make web-assets          # builds the tracking page, the SDK and the courier page (also after a pull)
make dev                 # the stack; add COMPOSE_OVERRIDE=<file> if your Docker builds have no network
make lan-https           # CA + certificate for the LAN address, courier page build, HTTPS services
```

`make lan-https` finds the LAN address from the default route (set `DT_LAN_IP=192.168.x.y` if it picks the wrong one), uses `mkcert` if it is installed (else `openssl`), keeps the CA in `build/lan/ca/` and prints the address. It also points the gateway's `COURIER_URL_BASE` at `https://<LAN-IP>:8443` (saved in `build/lan/lan.env`, which later `make` runs pick up, so `make dev` keeps the setting). The listeners bind to the LAN address only, and the API listener forwards only `/v1/courier/`.

The laptop's firewall must allow inbound TCP 8443, 8444 and 8445 from the LAN (for example `sudo ufw allow from 192.168.1.0/24 to any port 8443:8445 proto tcp`).

## 2. Trust the CA on the iPhone (once)

1. On the iPhone (same Wi-Fi), open Safari at `http://<LAN-IP>:8445/dt-local-ca.crt` (printed by `make lan-https`). Tap **Allow** when asked to download a configuration profile.
2. **Settings > General > VPN & Device Management** > the downloaded profile "DT local development CA" > **Install** (passcode, then Install again).
3. **Settings > General > About > Certificate Trust Settings** > switch on **DT local development CA** under "Enable full trust for root certificates" > Continue.
4. Check: Safari at `https://<LAN-IP>:8443/` shows the courier page's welcome screen (it says the link is missing or expired, which is right) with no certificate warning.

The CA is kept between runs, so this is a one-time step. If you delete `build/lan`, you get a new CA and must repeat it (and remove the old profile in VPN & Device Management).

## 3. Make the links

```sh
make courier-link
```

It creates a fresh test tenant, a courier `dev-courier`, a direct delivery near Amsterdam (pickup policy "any"), the assignment and a link session, and prints two links:

- `courier:` the link for the phone. It works **once** and for **15 minutes**; opening it uses it up. Send it to the phone (AirDrop, Messages, or type it). Run `make courier-link` again for a new one.
- `tracking:` open it on the laptop (it uses `localhost`, so it must be the laptop that runs the stack).

The map tiles are the Amsterdam fixture. To see the courier on the map elsewhere, set the pickup near you: `DT_PICKUP=<lat>,<lng> make courier-link`.

## 4. Walk

On the phone: open the courier link, read the consent screen, tap **Start shift**, allow location (choose "While Using the App"; keep the screen on). Tap through At store, Picked up, Delivered while the laptop shows the tracking page. If the page is reloaded or the tab is killed, open `https://<LAN-IP>:8443/` to resume; the used link will not work again.

## Troubleshooting

- Phone cannot load `http://<LAN-IP>:8445/...`: the firewall, a guest Wi-Fi with client isolation, or a different network.
- Safari warns about the certificate: step 2.3 was skipped, or the LAN address changed (re-run `make lan-https`; the CA stays, so no new trust step).
- "Link expired or used": make a new link.
- Location denied: Settings > Privacy & Security > Location Services > Safari Websites > While Using the App.
- Back to localhost only: `make down`, `rm -rf build/lan build/courier-lan`, then `make dev`.
