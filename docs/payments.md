# Payments

Two gateways ship — **PayFast** and **SnapScan** — behind a small `payment.Gateway`
interface. A store enables either or both, and with both the checkout asks the shopper
which they would like to use. Adding a third is code and no migration: the order's
`gateway_*` columns are deliberately gateway-neutral.

The two are not the same shape, and the interface says so rather than pretending otherwise:

| | PayFast | SnapScan |
|---|---|---|
| Hand-over | A cross-origin **POST** of signed fields | A **link** the shopper opens, shown as a QR code beside it |
| Needs in the CSP | `form-action` | `img-src` |
| Notification proved by | Signature + source IP + a server-to-server echo + merchant id | HMAC in the `Authorization` header + an authenticated read of SnapScan's API + snap code |
| Sandbox | Yes, and it is the default | **None. Any configuration takes real money** |
| Refunds | The dashboard | The merchant portal |

`Handover` is what carries the difference, and a template switches on it. Everything after
the hand-over — the callback, the amount check, the replay guard, the stock transaction — is
identical for both, which is the whole point of the split.

## SnapScan

The shopper opens one URL. On a phone it opens the SnapScan app directly, and if the app is
not installed SnapScan's own page offers a card or Instant EFT; on a desktop the same URL is
rendered as a QR code to scan with a phone that is not the device reading the page. The
hand-over page shows both, always, and reorders them by viewport width — a wrong guess about
which device somebody is on would remove the only way they can pay, so neither is hidden.

Because nothing redirects the browser back, the page polls `GET /cart/checkout/status` — the
order's status, read under the cart cookie, granting nothing. Without JavaScript it shows the
waiting message and the confirmation still arrives by email.

### Setting it up

1. Ask SnapScan merchant support for your **snap code**, an **API key** and a **webhook
   authentication key**, and give them the webhook address:
   `BASE_URL` + `/payments/snapscan/callback`. Unlike PayFast's, it is configured on the
   account rather than sent with each payment.
2. Set `SNAPSCAN_SNAP_CODE`, `SNAPSCAN_API_KEY` and `SNAPSCAN_WEBHOOK_AUTH_KEY`. The server
   refuses to start with the first and not the others: a notification with nothing to check
   its signature against is an unauthenticated request that can mark orders paid.
3. Optionally ask them to enable the **Secure QR Payload** feature and set
   `SNAPSCAN_VALIDATION_KEY`. It signs the amount and the order reference in the payment URL
   so neither can be edited between this page and the scan. Without it, `strict=true` still
   fixes a minimum amount and refuses a repeat payment on the same order — both are sent
   whenever a key is configured, because they do different jobs.

**The QR image URL carries no redirect URLs**, and that is load-bearing rather than tidy:
SnapScan's image endpoint answers `403` with an HTML body when `s_url` or `f_url` is
present, and a browser then refuses the HTML as an image — Chrome reports
`ERR_BLOCKED_BY_ORB` in the console and shows nothing. `curl` sees a `403`, the markup is
perfect and every handler test passes, so only a real browser finds it. Nothing is lost: a
scanned code is paid in an app on another device, where there is no browser to redirect.

**There is no sandbox.** PayFast's safe default trains the opposite expectation, and there is
no equivalent here: the first real test is the smallest payment you are willing to make. A
local one also needs a tunnel, since SnapScan's servers have to reach the callback.

### How a SnapScan notification is authenticated

Two independent things must be true, and neither is sufficient alone:

1. **The body is signed with the webhook key.** `Authorization: SnapScan signature=<hash>` is
   an HMAC-SHA256 of the raw body — the whole form-encoded body including the `payload` key,
   not the JSON inside it — compared in constant time.
2. **SnapScan's API says the same thing.** The payment is read back from
   `/merchant/api/v1/payments/{id}` over an authenticated connection, and **the status and
   amount this store acts on come from that response**, never from the notification body.
   SnapScan's own documentation calls the webhook "an unauthenticated event stream" and
   points at the API for certainty; a shared secret is only as good as everywhere it has ever
   been copied, so one leaked key must not be a free order.

Then the snap code is checked against the configured one — the equivalent of PayFast's
merchant-id check — and the amount is taken from `requiredAmount` rather than `totalAmount`,
because a tip on a tipping-enabled account makes the total larger than the figure this store
asked for and would otherwise read as a mismatch.

The handler then does what only it can, identically for both gateways: find the order, refuse
a notification for an order placed through a *different* gateway, check the amount against the
order's own total, and stop a replay from decrementing stock twice.

## PayFast

### Setting it up

1. Get a merchant id and key from the [PayFast dashboard](https://sandbox.payfast.co.za) —
   the sandbox's have no relationship to a live account's.
2. Set a **salt passphrase** in the dashboard and put the same value in
   `PAYFAST_PASSPHRASE`. Set on one side only, every signature fails.
3. Leave `PAYFAST_SANDBOX=true` until a full sandbox payment has worked end to end.
4. Make sure PayFast's servers can reach the callback. `notify_url` is derived from
   `BASE_URL`, which on a laptop is `localhost` and unreachable from the internet — so local
   testing needs a tunnel, and `PAYFAST_NOTIFY_URL` is where its hostname goes.

Then, in order: place an order, pay it on the sandbox, and check that the order is `paid` and
stock has moved. Replaying the captured notification body with `curl` must not move stock a
second time.

### Going live

**`PAYFAST_SANDBOX` defaults to `true` on purpose**, so that nobody's first afternoon with
this project charges a real card. The cost of that default is the mirror mistake: a
deployment that never sets it takes no money and looks like it works. Set it explicitly
wherever you deploy — the [Compose deployments](deploy/README.md) refuse to start without
it, for exactly this reason.

Switching it off is two changes, not one. The merchant credentials must be your own: the
server **refuses to start** with `PAYFAST_SANDBOX=false` and PayFast's published sandbox
merchant id, because that combination signs every payment with a key printed in PayFast's
documentation.

One more, if you deploy behind any proxy or managed platform: **`CLIENT_IP_SOURCE` must
describe it**, or the source-IP check below compares PayFast's ranges against your load
balancer's address and rejects every genuine notification — money taken, nothing recorded.
Use `forwarded` behind a proxy that *replaces* `X-Forwarded-For`, and `cloudflare` where
Cloudflare is the only way in. It must stay `remote` when nothing in front of the server
sets either header, since a client could then claim any address it liked.

Behind Cloudflare specifically, `forwarded` is the wrong answer rather than a
conservative one: Cloudflare **appends** to `X-Forwarded-For`, so its leftmost entry is
whatever the client sent. `CF-Connecting-IP` holds one address and is the header to read
— and is only worth trusting where nothing can reach the origin except through Cloudflare.

### How a PayFast notification is authenticated

The customer's browser returning to `return_url` proves nothing. The **ITN** — PayFast's
form-encoded POST to `notify_url` — is the only statement about a payment this store trusts,
and it passes four independent checks before anything happens:

1. **The signature recomputes** over the fields exactly as received, in the order received.
2. **The source IP** is one of PayFast's published ranges.
3. **PayFast confirms it**, when the exact bytes received are posted back to
   `/eng/query/validate`.
4. **The merchant id** is ours.

None of them is sufficient alone. The signature can be produced by anyone holding the
passphrase, an IP can be spoofed or shared with whoever else is behind the same proxy, and the
server-to-server check proves the data is PayFast's but not that it was meant for this store.

Then the handler does what only it can: find the order, check the amount against the order's
own total, and stop a replay from decrementing stock twice.

**The callback always answers `200`.** A gateway retries anything else, and a notification
that fails validation is not "try again later" — it is forged or broken, and neither improves
on the third attempt. Rejections are logged in full, naming the check that failed, and
dropped. It is also outside the CSRF group by *not being in it* rather than by an exempt-path
string that has to keep matching the route.

### The PayFast signature, and why it is spelled out in code

Three details account for nearly every PayFast integration failure, and
[`internal/payment/payfast`](../internal/payment/payfast) says so in its package comment:

- **The field order is the order they were submitted in, not alphabetical.** Sorting produces
  a signature PayFast rejects. This is why `payment.Field` is a slice and never a map anywhere
  near a signature.
- **`urlencode` is PHP's**, which every reference implementation uses. Go's `url.QueryEscape`
  is nearly the same and differs over `~`, and one character is a failed signature with no
  diagnostic beyond "mismatch".
- **Outgoing and incoming disagree about blank fields.** They are excluded when building the
  redirect form and *included* when verifying a notification, because that is what PayFast's
  own code does in each direction. Building the form sidesteps it by not submitting blanks at
  all.

`TestPayFast_SignatureMatchesKnownVector` pins both the parameter string and its digest.
**Put that string through [PayFast's signature tool](https://developers.payfast.co.za) before
taking real money** — no test suite can do that step, and the cost of skipping it is every
payment being rejected.
