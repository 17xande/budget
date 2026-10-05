// The page script, bundled by `deno task build` into
// internal/handler/static/dist/app.js and embedded in the Go binary.
//
// Everything here is progressive enhancement: every page works without it, and
// the CSP (script-src 'self') means nothing in it may rely on inline handlers.

import { localiseTimes } from "./localtime.ts";

localiseTimes(document);

// htmx swaps new server-rendered fragments in; localise those too.
document.addEventListener("htmx:afterSwap", (e) => {
  const target = (e as CustomEvent<{ target: Element }>).detail?.target;
  if (target) localiseTimes(target);
});
