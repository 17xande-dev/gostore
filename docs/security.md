# Security

## CSRF

Every state-changing request — admin or cart — needs a
[nosurf](https://github.com/justinas/nosurf) token, submitted as a `csrf_token` form field
(the `{{template "csrf" .CSRFToken}}` partial) or an `X-CSRF-Token` header. A request without
one gets `403`.

CSRF is mounted over groups of routes rather than the whole server, for a reason worth
knowing before adding any: nosurf sets a token cookie on every response it handles, and a
catalog fetched from another origin must stay cookie-free. So `/admin`, `/cart` and
*first-party* catalog pages go through it — the last of those because the product page
carries an add-to-cart form — while the same catalog pages fetched cross-origin do not.
Grouping also means the payment callback, which cannot carry a token and must never require
one, will be exempt by not being in a group at all, rather than by an exempt-path string that
has to keep matching the route.

The trap this design walks into, if you extend it: **a page rendered outside the CSRF layer
gets an empty `.CSRFToken`**, and every form on it is then refused with a `403`. Any new page
carrying a form has to be inside one of these groups.

nosurf also requires that an unsafe request identify its origin, via `Sec-Fetch-Site`,
`Origin` or `Referer`. Browsers always send at least one; **`curl` does not**, so manual
testing needs `-H "Origin: http://localhost:8080"` or the request is rejected before the
token is even examined.

The origin nosurf compares against is built from the request's `Host` and a scheme it
assumes is `https` unless told otherwise, so the scheme is taken from **`BASE_URL`** rather
than from the connection: behind a TLS-terminating proxy the connection is plain HTTP while
the browser's origin is `https`. Getting `BASE_URL` wrong therefore breaks every admin form
with a `403`, not just absolute links.

## Hardening

### Rate limits

Per client IP, on four surfaces, with a token bucket from
[`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) and the keying and
eviction written here — the algorithm is the part with the clock edge cases already found
in it, and a bucket per client with bounded memory is where the decisions are.

| Route | Default | Why |
|---|---|---|
| `POST /admin/login` | 10/min | Brute force. argon2id's cost makes each attempt expensive, but cost is not a limit |
| `POST /cart/checkout` | 20/min | Order-row spam, loose enough that double-clicking never trips it |
| `POST /payments/{gw}/callback` | 120/min | **The reason the limiter exists**: unauthenticated, and every accepted request makes the store POST to the gateway — an amplifier |
| `GET /cart/checkout/status` | 30/min | The QR hand-over page's poll. Cheap per request, but an open page asks all afternoon |

The burst is a third of the allowance (minimum 2), so `10/min` means three attempts
immediately and then one every six seconds. A refusal is `429` with `Retry-After`. Limits
are applied on the line that registers each route, not wrapped around a prefix, for the
same reason `RequireAdmin` is: a prefix wrapper is one refactor away from silently not
covering a new route.

**The callback's `429` is not a contradiction of the always-`200` rule.** `200` means
*read and decided*, so a gateway does not retry a forgery. A throttled request has not
been read, and a retry is exactly what should happen — hence the limiter sits in front of
the handler and answers `429`, which PayFast reads and honours.

Only the POST on `/admin/login` is limited. Limiting the GET would lock an operator out of
the page carrying the message explaining why.

Idle buckets are evicted on a lazy sweep during an ordinary request, so there is no
goroutine to own and shut down for a map that is usually tiny, and the map cannot grow
without bound as an attacker cycles addresses.

**Catalog search is deliberately not limited**, and it is worth being explicit since it is the
one read that costs more than a primary-key lookup: every surface above is a `POST`, and
`GET /products` is none of them. A search is a bounded index scan over a small table, it holds
no lock and writes nothing, and the page it returns is cacheable — so the limiter would mostly
be throttling a crawler doing something harmless. That reasoning depends on the catalog staying
small; a store whose search starts showing up in slow-query logs should give `/products` its
own bucket, which is one line where the route is registered.

### Password hashing

argon2id, via `x/crypto/argon2` — no new dependency, since `x/crypto` was already here for
bcrypt. Parameters are RFC 9106's second recommendation: 64 MiB, three passes, four lanes,
encoded into the hash as a standard PHC string:

```
$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
```

Because the parameters live in the hash, raising them later needs no migration: existing
hashes keep verifying at their own settings, and the next password set through the admin —
or through `make hashpw`, which is now only the lockout-recovery path — is written with the
new ones.

**A bcrypt hash still verifies.** `CheckPassword` dispatches on the prefix, so a hash
carried over from an older deployment keeps working and moves to argon2id the next time that
password is set. New hashes are argon2id only.

Two details that are defence rather than decoration:

- Verification **caps the memory a stored hash may request** at 1 GiB. Without that, a
  hand-edited hash claiming `m=4194304` would try to allocate four gibibytes on the first
  login attempt — a denial of service delivered by a typo.
- A hash is **parsed on the way in**, by every store method that takes one, so a malformed
  one is refused where the caller can say so rather than becoming an account that can never
  sign in with nothing in the logs to explain it.

### Response headers

```
Content-Security-Policy: default-src 'self'; img-src 'self' <bucket> <qr gateway>;
  font-src 'self' <font origins>; style-src 'self' <font origins>; script-src 'self';
  form-action 'self' <form gateway>; base-uri 'none'; object-src 'none';
  frame-ancestors <embed origins or 'none'>
Permissions-Policy: geolocation=(), camera=(), microphone=(), payment=()
Referrer-Policy: strict-origin-when-cross-origin
X-Content-Type-Options: nosniff
Strict-Transport-Security: max-age=63072000; includeSubDomains   (https deployments only)
```

**Digital downloads add no directive**, which is worth stating because the download bucket
looks like something that ought to be named here. A buyer's download is a top-level
navigation to a signed URL, and no fetch directive governs one — `connect-src` would only
come into it if the browser uploaded straight to the bucket, which is exactly the design
that was declined.

`img-src` is `'self'`, the bucket, and — when a QR gateway like SnapScan is configured — the
origin it serves payment codes from. A *product* image is always bytes this store holds; a
payment code is not an image of anything this store has, and hotlinking it is what keeps the
QR the gateway's problem rather than a rendering job here.

The angle-bracketed placeholders are the only external origins any directive gets, each one
named by a deployment: the bucket, the payment gateways, the embedders, and a font service.
There is no `'unsafe-inline'` anywhere — which is worth knowing before
writing a theme:

- **`font-src` and `style-src`** — both `'self'` unless `FONT_ORIGINS` is set, which is the
  only knob that opens two directives at once. A hosted font is two fetches: the stylesheet
  declaring the fonts, then the files it names. See [Web fonts](theming.md#web-fonts).
- **`style-src 'self'`** — it used to carry `'unsafe-inline'`, because restyling through
  `TEMPLATE_DIR` had no other legal way to apply CSS: there was no stylesheet and no way for
  an adopter to add one. Both exist now — a bundled `styles.css` and `STATIC_DIR` to replace
  it — so the concession is gone. **An overriding template cannot use `style` attributes or
  `<style>` blocks**; put the CSS in a stylesheet in `STATIC_DIR`. (Email bodies still use
  inline styles, because no CSP has ever applied to them.)
- **`script-src 'self'`** — same rule for scripts. A theme's JavaScript is a `.js` file in
  `STATIC_DIR`, referenced with `{{asset "yours.js"}}`; an inline `<script>` is simply not
  run.

HSTS is sent only when `BASE_URL` is `https://`: browsers ignore it over plain HTTP, and
sending it from a development server would pin a rule making the next plain-HTTP project
on that port unreachable. There is deliberately no `preload` directive — that list has a
slow exit, and it is the operator's decision rather than this project's.

### Overselling

Stock is taken at payment, never reserved at checkout, so two shoppers can both reach a
payment page for the last item and both pay. The second order is still recorded **paid** —
the money was taken, and refusing to record it would lose the sale *and* still be oversold
— and `orders.oversold` is set in the same transaction, so an order is never
paid-but-unflagged.

It shows as `OVERSOLD` in `/admin/orders` and as a prominent block on the order page, as
well as in the owner's notification email. Before this phase it existed only in the logs
and that email, which is the wrong place for something needing reconciliation: an email is
read once and a log is not read at all.

Nothing here refunds anything. Refunds happen in the gateway's dashboard, because this
schema models a forward payment only — see the plan's note on when to reconsider
off-the-shelf.

### Cart cleanup

Carts untouched for `CART_TTL_DAYS` are deleted on boot and then daily, by a goroutine in
the server process. It runs in every instance rather than being elected to one, and that is
fine because the work is a single idempotent `DELETE`: two instances produce the same end
state as one, and the second finds nothing to do. Electing a leader would need coordination
this store has no other use for, and a cron container would break the one-binary
deployment story.

A failed sweep is logged and retried at the next tick. A cleanup that fails is a table that
grows a little longer, which is not worth waking anyone for.
