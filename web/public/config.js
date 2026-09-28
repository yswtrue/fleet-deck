// FleetDeck runtime configuration (optional).
//
// The SPA reads this file before the app bundle starts, so a standalone static
// deployment (any file server / CDN) can point at a hub on any domain — just
// edit this file next to index.html after deploying:
//
//   window.__FLEETDECK_HUB_URL__ = "https://hub.example.com";
//
// Leave it unset to use the page's own origin — correct when the hub serves
// the UI itself (default) or when a reverse proxy fronts both on one origin.
window.__FLEETDECK_HUB_URL__ = window.__FLEETDECK_HUB_URL__ || undefined
