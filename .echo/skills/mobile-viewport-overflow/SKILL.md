---
name: mobile-viewport-overflow
description: Why the Echo SPA page scrolls/zooms out past the app on mobile, and how to find and prevent viewport overflow from escaped absolute-positioned badges.
triggers:
    - page scrolls past app
    - mobile overflow
    - plugin count badge
    - shrink to fit
    - scrollWidth innerWidth
    - mobile-nav-tabs
---

# Mobile viewport overflow in the Echo SPA

## Symptom
On phones the whole page can scroll/pan down and right past the app, which renders smaller than the screen with empty space around it. A stray plugin count badge may float at the screen edge.

## Root cause chain
1. `.mobile-nav-tabs` (in `web/css/app.css`, `@media (max-width: 720px)`) is the horizontal scroller for the mobile nav tabs. Its content (`.tabsScrollW` ≈ 400-450px) routinely exceeds its width (~222px), so later tabs sit beyond the right edge, clipped.
2. Any `position: absolute` decoration inside those tabs (e.g. `.plugin-count`, which has no `top`/`left` and relies on static position) resolves its containing block to the nearest positioned ancestor. `.mobile-nav-tab` had no `position`, so the containing block was `.mobile-bottom-nav` (`position: relative`) — an ancestor OUTSIDE the `.mobile-nav-tabs` scroller. Per CSS overflow rules, an abspos element whose containing block is an ancestor of a scroller is NOT clipped by that scroller.
3. The badge therefore renders past the nav's right edge and widens the document (e.g. 459px vs 360px viewport).
4. Mobile Chrome shrink-to-fit then zooms the layout viewport out to the document width (innerWidth becomes 459), so the app lays out at 360px inside a 459px viewport: pannable empty space right/bottom, everything looking "smaller than the screen".

## Rule
Any absolutely-positioned badge/pin inside a scroll-clipped container must have its nearest positioned ancestor INSIDE that container. Fixed by adding `position: relative` to `.mobile-nav-tab` (app.css mobile block).

The same rule applies to `.mobile-nav-pill`, `.plugin-notice`, and any future decorations in `.mobile-nav-tabs` or other overflow containers.

## Debugging technique
Reproduce with an isolated instance (no auth risk to the user's data): build the Go binary (embeds `web/dist` — run `npm run build` in `web/` FIRST or you verify stale CSS), run it with `-port=<free> -data=<temp>/echo.json`, drive with Playwright chromium at `viewport 360x800, isMobile: true, hasTouch: true`, complete setup, create a workspace + stage/approve a builtin plugin (`POST /api/plugins/stages {source:{type:"builtin",builtin:"notes"}}` then `POST /api/plugins/stages/{id}/approve {scope:"global",enable:true}`), then evaluate `document.documentElement.scrollWidth/scrollHeight` vs `innerWidth/innerHeight`. `innerWidth` GROWING beyond the viewport width is the shrink-to-fit signature; scan all elements for rects past the viewport to find offenders (skip `position: fixed`, ignore elements clipped by an ancestor scroller).
