# Universal Commerce Protocol (UCP) in OwnShop AI

With UCP, any AI agent can discover the shop, search the catalog, check out and look up its orders.
It is built in, **off by default**, and served **on this computer only** (127.0.0.1). Turn it on in
Settings with "Let AI agents shop here". While it is off, every UCP route answers 404.

Code: `ucp.go` (discovery, catalog, checkout, order), `ucp_mcp.go` (MCP transport), tests in `ucp_test.go`.

## Spec followed

- Release **v2026-08-25** of github.com/Universal-Commerce-Protocol/ucp, tag `v2026-08-25`,
  commit `87dd4b1fcff9105d68173120897237fa784c74ef`. This is the latest tagged release as of 2026-11-22.
- Spec pages: https://ucp.dev/2026-08-25/specification/overview, `.../shopping/checkout` (plus `/rest` and `/mcp`),
  `.../shopping/catalog/search`, `.../shopping/order`, `.../shopping/extensions/fulfillment`, `.../payment/extensions/terms`.
- Schemas: `source/schemas/` at that tag (`profile.json`, `ucp.json`, `service.json`, `capability.json`,
  `payment_handler.json`, `shopping/checkout.json`, `shopping/catalog_search.json`, `shopping/order.json`,
  `shopping/fulfillment.json` and `shopping/types/*`, `common/types/*`). MCP tools and arguments come from
  `source/services/shopping/mcp.openrpc.json`. REST paths come from `source/services/shopping/rest.openapi.json`.
- **Why not "2026-01-11"?** That was the first release, and it has no catalog capability: `catalog_search.json`
  first appears in v2026-04-08. Releases since then also changed the checkout and order shapes: `payment` is no longer a required checkout field, and the order gained a required `currency` plus `label` and `messages`. OwnShop AI follows
  the current release, and the profile states its version (`ucp.version` = `2026-08-25`).

Every field name comes from those schemas. Whatever OwnShop AI adds sits under its own reverse-domain name,
`com.deemwar.ownshop.*`. The spec allows this: objects are open (`additionalProperties: true`) and reverse-domain
names are reserved for extensions.

## Endpoints (base `http://127.0.0.1:2007`, or whatever port OwnShop AI picked)

| What | REST | MCP tool |
|---|---|---|
| Discovery profile | `GET /.well-known/ucp` | (n/a) |
| Catalog search (`dev.ucp.shopping.catalog.search`) | `POST /ucp/catalog/search` | `search_catalog` |
| Create checkout (`dev.ucp.shopping.checkout`) | `POST /ucp/checkout-sessions` | `create_checkout` |
| Get checkout | `GET /ucp/checkout-sessions/{id}` | `get_checkout` |
| Update checkout | `PUT /ucp/checkout-sessions/{id}` | `update_checkout` |
| Complete checkout | `POST /ucp/checkout-sessions/{id}/complete` | `complete_checkout` |
| Cancel checkout | `POST /ucp/checkout-sessions/{id}/cancel` | `cancel_checkout` |
| Get order (`dev.ucp.shopping.order`) | `GET /ucp/orders/{id}?phone=…` or `?email=…` or `?checkout_id=…` | `get_order` (`id` plus `buyer:{phone_number|email}` or `checkout_id`) |
| MCP endpoint | `POST /ucp/mcp` | `initialize`, `ping`, `tools/list`, `tools/call` |

The REST paths are the spec's paths under the endpoint the profile advertises (`<base>/ucp`). They sit under
`/ucp` because the owner's own pages already use `/orders`.

### How things map

- **Profile**: `ucp.version`, `ucp.services["dev.ucp.shopping"]` (one `rest` binding and one `mcp` binding, each
  with `endpoint`, `spec` and `schema`), `ucp.capabilities` (catalog.search, checkout, fulfillment extending
  checkout, order) and `ucp.payment_handlers`. The shop's name, address, currency and what it sells go under
  `com.deemwar.ownshop.merchant`, because the profile schema has no merchant-name field.
- **Catalog**: one product per active OwnShop AI item. The product `id`, the variant `id` and `sku` are all the SKU
  (`p<rowid>` if the SKU is blank, or shared by two products, since only the product name is unique). Also included:
  - `price_range`, and the variant's `price` (`amount` in **minor units**, per `amount.json`, so INR 9.50 is
    `950`), plus `currency`.
  - `availability`: `available`, plus `status` as `in_stock`, `out_of_stock` or `no_free_time`.
  - `media` (the photo at `/products/{sku}/photo`) when the shop has a photo.
  - `metadata["com.deemwar.ownshop"]` holds `{kind, stock}` for a product, or `{kind, minutes, free_starts}` for a
    service. The spec has no stock count, so stock goes here.
  - Search is a word match on name, description, keywords and SKU. It supports `filters.price.min/max` and
    `pagination.limit/cursor`.
- **Checkout**: you send `line_items[{item:{id}, quantity}]`, an optional `buyer{first_name, last_name, email,
  phone_number}`, and `fulfillment.methods[0]` with `type:"shipping"` (local delivery), one
  `destinations[]` postal address, and `groups[0].selected_option_id`. The response follows `checkout.json`:
  `id`, `status`, `line_items` with `totals`, `currency`, `totals` (subtotal and total), `links`, `messages`,
  `expires_at` and `fulfillment`.
  - The owner's **delivery slots** are the `options` of the one fulfillment group, with ids `slot_<n>`, as the
    fulfillment extension describes.
  - `status` is `incomplete` while something is missing. Each gap is an error message with
    `severity:"recoverable"`: `buyer_name_required`, `buyer_contact_required`, `address_required`,
    `fulfillment_option_required`, `booking_required`, `booking_unavailable` or `out_of_stock`. Otherwise
    `status` is `ready_for_complete`. The rules match the chat bot: a delivery address for products, a slot when
    the shop has slots, and a free time for services.
- **Complete** places a normal OwnShop AI order using the same Store calls as the chat bot and walk-in bookings
  (`Customer`, `AddItem`, `SetSlot`/`SetBookAt`, `Confirm`). Stock checks and cuts, time holds and invoice
  numbers are therefore identical. The order's channel is `ucp`, and the Orders page labels it "From an AI agent
  (UCP)". The response has `status:"completed"`, `order{id, label (invoice), permalink_url}`, and
  `com.deemwar.ownshop.payment`, which holds:
  - `status:"unpaid"`, `reference`, `invoice`, `amount` and `currency`
  - `upi_link` (a per-order UPI link with the amount and order filled in)
  - `payment_link` and `bank_transfer`
  - `payment_qr_url` (the owner's uploaded QR image at `/payment-qr`) when one is set
  - `instructions`, the same text the chat bot sends. It is also repeated as an `info` message.

  A retry of a completed checkout returns the same order and changes nothing. If stock ran out or the time was
  taken in the meantime, complete returns the checkout as `incomplete` with a message, and no order is created.
- **Order** follows `order.json`: `id`, `label` (invoice), `checkout_id`, `permalink_url`, `line_items` (with
  `quantity{original,total,fulfilled}` and `status`), `fulfillment.events`, `currency` and `totals`. The owner's
  status maps like this:
  - Packed becomes a `processing` event, Shipped a `shipped` event, Delivered a `delivered` event.
  - Cancelled becomes a `cancellation` adjustment, and the line items are `removed`.

  The OwnShop AI status and the label the chat bot uses ("Waiting for payment", "Payment received", ...) appear in
  `com.deemwar.ownshop.order.status_label` and in an `info` message. `order.json` has no order-level status field.
  When the owner clicks a status button on an agent's order, the order moves as usual. No chat channel exists
  for it, so the Orders page says the customer message was NOT sent, and the agent sees the change on its next
  `get_order`.
- **Services** have no stock and no address. The booking start is set with
  `"com.deemwar.ownshop.booking": {"start": "2026-11-21T10:00"}`. The checkout response echoes it with `minutes`,
  a `label` and, while it is missing or taken, `free_starts`. A start must be free, in the future, and within the
  shop's "days ahead", the same window the chat bot offers. v2026-08-25 has no booking-time field (its schemas
  cover only common, shopping and transports), so this is OwnShop AI's extension.

## Payment: offline, owner-confirmed

OwnShop AI never touches money. UCP payment handlers describe ways to pass a credential or token to a processor.
The spec has **no handler for "pay outside the agent, the merchant confirms by hand"**. The closest thing is the
Payment Terms extension (`dev.ucp.common.payment.terms`). It describes *when* money is due, not an offline
method, and it still assumes an instrument. OwnShop AI therefore:

- declares its methods as business handlers under its own name, `com.deemwar.ownshop.offline_payment`, with ids
  `upi`, `bank_transfer`, `payment_link` and `payment_qr`. Only the methods the owner set in Settings are listed.
  Each has `available_instruments:[{type}]` and no credential config. If none is set, `payment_handlers` is `{}`.
- accepts complete with **no `payment`** (a deviation: the spec marks `payment` required on complete). If
  `payment.instruments[].handler_id` is sent, it must be one of the declared ids. No credentials are read or stored.
- leaves the order unpaid until the owner clicks Payment received. `get_order` then reports `paid: true` and
  "Payment received".

## Safety

- Served only where OwnShop AI binds, which is 127.0.0.1 by default (`app.go` `addrFor`, `main.go --addr`). Nothing
  here changes the bind. The routes are unauthenticated because they are local.
- Off means 404 on every UCP route, including `/ucp/mcp`.
- Local only, checked in code as well: a request whose `Host` is not localhost, 127.0.0.1 or ::1 gets 403. This
  covers `--addr 0.0.0.0` and a tunnel, which would otherwise publish unauthenticated routes.
- Browser protection: a request with an `Origin` that is not localhost gets 403. A POST or PUT with a body must be
  `Content-Type: application/json` (415 otherwise), so a web page cannot post a plain form to localhost. Complete
  and cancel may be sent with no body.
- Limits:
  - 64 KB per body, 20 line items, quantity 1 to 99.
  - Names up to 60 characters, email up to 120, address fields up to 200.
  - Phone numbers must have 6 to 15 digits.
  - Item ids are checked against the live catalog.
- Rate limit: 30 checkout creations a minute for the whole shop (every caller is 127.0.0.1), and at most 200
  open (not completed or canceled) sessions. Over the limit you get HTTP 429 `rate_limited`, or JSON-RPC `-32000`
  on MCP.
- Sessions live in memory for 6 hours and never touch the orders table, so abandoned checkouts burn no order or
  invoice numbers. A restart forgets every session, completed ones too: retrying complete then returns
  `not_found`, but the order itself stays reachable with `get_order` (by `checkout_id` or buyer contact).
- When UCP is off, a wrong HTTP method on a UCP path gets Go's 405 rather than 404. Only the path's existence leaks.
- Buyers are kept separate from chat customers (customer key `ucp:+<digits>` or `ucp:<email>`), so an agent
  cannot rename or re-address a WhatsApp or Telegram customer.
- **Order lookup** needs the order id plus the buyer's phone or email from checkout, or the checkout id (128
  random bits, known only to its creator). It only finds orders placed over UCP. A wrong phone, a missing order
  and a chat order all get the same `not_found` answer.

## How an agent connects today (local)

1. In Settings, tick "Let AI agents shop here" and Save.
2. Any MCP client with HTTP transport: add the server URL `http://127.0.0.1:2007/ucp/mcp`.
   Use the port the shop page shows if OwnShop AI moved off 2007.
3. Or use REST/discovery: `curl http://127.0.0.1:2007/.well-known/ucp`, then follow `services["dev.ucp.shopping"]`.

Typical flow: `search_catalog`, then `create_checkout`, then `update_checkout` until `status` is
`ready_for_complete`, then `complete_checkout`. Pay using `com.deemwar.ownshop.payment`. Check later with
`get_order` (`id` plus `buyer.phone_number`).

## Gaps against the spec (v2026-08-25)

- **Signing and keys**: no HTTP Message Signatures, no `keys` in the profile, and no signed responses
  (`signatures.md`).
- **Negotiation**: the `UCP-Agent` header and `meta["ucp-agent"]` are accepted and ignored. OwnShop AI never fetches
  the agent's profile and returns its full capability set. `idempotency-key` is not tracked; complete is
  idempotent per checkout id instead.
- **Not implemented**:
  - catalog lookup (`dev.ucp.shopping.catalog.lookup`: `/catalog/lookup`, `/catalog/product`, `lookup_catalog`,
    `get_product`), so it is not advertised
  - cart (`dev.ucp.shopping.cart`)
  - discount, buyer consent and identity linking
  - order webhooks (the platform `webhook_url`); agents poll `get_order`
  - A2A and embedded transports
- **Payment**: offline handlers under OwnShop AI's own name, and `payment` is optional on complete (see above).
- **Booking time and stock count**: OwnShop AI extensions (`com.deemwar.ownshop.booking`, `metadata["com.deemwar.ownshop"]`).
- **Order lookup auth**: `get_order` takes extra arguments (`buyer`, `checkout_id`) that the binding does not
  define. The spec's `get_order` takes only `id` and assumes an authenticated platform.
- **Fulfillment**: shipping (local delivery) only, one destination, and delivery slots as the group options with
  a zero cost. Fulfillment event times are the order's creation time, because OwnShop AI does not record when each
  status changed.
- **Currency**: minor units use the ISO 4217 exponent (2 by default; 0 and 3 for the usual exceptions). The
  owner's currency text is upper-cased. If it is not a 3-letter code, it is passed through as-is, which does not
  match `^[A-Z]{3}$`.
- **MCP**: JSON responses only (no SSE stream; `GET /ucp/mcp` returns 405), no JSON-RPC batches, no MCP session id.
- **continue_url** is not set (there is no web checkout to hand off to).

## TODO: serving it publicly

Today it is local only, on purpose. Before any agent outside this computer can reach it:

1. **Exposure**: a tunnel (for example Cloudflare Tunnel to a deemwar subdomain) or deemwar hosting in front of
   the laptop, over HTTPS, so `/.well-known/ucp` resolves on the shop's own domain. The profile builds
   endpoints from the request host, but it uses `https` only when TLS ends inside ownshop. Behind a tunnel it must
   trust `X-Forwarded-Proto` or use a configured public base URL (not done yet).
2. **Auth**: the routes have no auth because they are local. Public serving needs platform authentication (an API
   key or OAuth per agent platform) and UCP request signing with a published `keys` set. Without it, keep
   `get_order` behind the buyer and checkout-id check.
3. **Abuse limits**: per-client rate limits (a per-IP limit is meaningless today, since every caller is local),
   and persisting sessions if restarts matter.
4. **Origin policy**: replace the localhost-only Origin check with the public host.
5. **Webhooks**: order status pushes to the platform `webhook_url` once there is somewhere to send them.
