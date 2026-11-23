// www.ownshopai.store and the old ownshopai.deemwar.com answer with a redirect to the main site.
const MAIN = "ownshopai.store";
export async function onRequest({ request, next }) {
  const url = new URL(request.url);
  if (url.hostname === "www." + MAIN || url.hostname === "ownshopai.deemwar.com") {
    url.hostname = MAIN;
    url.protocol = "https:";
    return Response.redirect(url.toString(), 301);
  }
  return next();
}
