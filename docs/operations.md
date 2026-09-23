# Operations

## When something goes wrong

Every HTML surface answers a failure with a rendered page rather than a line of plain
text: an unknown URL, a withdrawn product, a form whose token has expired, a request
that came too fast, a fault on the server. Three pages cover it — `pages/not_found.gohtml`
for 404, `pages/error_client.gohtml` for the rest of the 4xx range,
`pages/error_server.gohtml` for 5xx — and all three are overridable from `TEMPLATE_DIR`.
They are three files rather than one because they say genuinely different things, and a
shop should be able to reword one without touching the others; the reference block they
share is `partials/error_reference.gohtml`.

**In development the page says what broke; in production it does not.** The signal is
`BASE_URL`: an `https://` origin is production and the page shows only a reference, and
anything else shows the Go error as well. That is the same signal `Secure` cookies and
HSTS already use, so "is this production" has one answer rather than three. The error
string names tables, columns and constraints, which is reconnaissance in front of a
stranger and a first diagnosis in front of whoever is writing the code.

**Every response carries a request id**, echoed as `X-Request-Id` and printed on the
error page as a reference. The same id is on every log line for that request, so "I got
an error and it said 7f3a9c2e" is enough to find the one line that says why. An id
arriving in `X-Cloud-Trace-Context` (Cloud Run sets one on every request) or
`X-Request-Id` is adopted rather than replaced, so the store's logs and the platform's
name the same request.

Two deliberate exceptions to all of the above:

- **Byte endpoints stay plain.** A missing `/static/…` or `/images/…` answers with Go's
  one-line 404, because nothing reads an HTML page out of an `<img>` tag.
- **A broken theme still answers correctly.** If an overridden error template will not
  render, the plain text is sent with the same status — a 500 that fails to render must
  not become an empty 200.

### Errors and htmx

htmx does not swap `4xx`/`5xx` responses by default, which quietly discarded every
refusal this store sends: the cart answers "Only 2 of that option left" as a `409`
carrying the fragment the page asked for, and the browser dropped it, so a shopper
clicked *Add to cart* and watched nothing happen. `partials/document.gohtml` therefore
configures `responseHandling` to swap errors too, in the one `<head>` both layouts use.

That makes one rule load-bearing, and it is worth knowing before adding a handler: **an
error response either fills the target it was aimed at, or replaces the document.** A
refusal that belongs in its target renders a fragment; a whole error page sends
`HX-Retarget: body` so it is not pasted into the cart-count span. `HX-Refresh` is the
third option, used where a reload is both the explanation and the fix — an expired CSRF
token, or an admin session that has ended.

## Logging

**JSON to stdout, and nothing else.** No log file, no log table, no agent. That is the
interface every container platform already reads: `docker compose logs`, `make logs`, or
Cloud Logging on Cloud Run with no configuration at all.

Errors are deliberately **not** written to the database. It is the most likely thing to
be broken during an incident, so a logger that needs it fails exactly when it is wanted;
an error storm becomes a write storm; and retention becomes somebody's job. What the
schema does keep is durable *facts* that need acting on — `orders.oversold`,
`orders.gateway_payload` — which is a different thing from diagnostics.

`LOG_FORMAT=gcp` renames `level` and `msg` to `severity` and `message`. On Google Cloud
that is the difference between a working `severity>=ERROR` filter and one that silently
matches nothing, because every line files under `DEFAULT` without it. It is opt-in
because the convention is Google's, and the default output is unchanged.

What is deliberately not here yet: metrics, tracing, and alerting. The volume worth
watching is small — a healthy store logs a dozen lines at boot, one a day for the cart
sweep, and then only per-event lines — and the `ERROR` stream is already high-signal:
an unrecorded payment, an oversold order, a receipt that did not send.
