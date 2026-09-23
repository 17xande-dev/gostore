# Orders and email

Two pages, both read-only:

| Route | Shows |
|---|---|
| `GET /admin/orders` | Recent orders, newest first |
| `GET /admin/orders/{id}` | What to pack, where it goes, and what the gateway said |

**There are no buttons on either page, deliberately.** An order records something that
happened, and the only thing allowed to change one is an authenticated gateway notification.
A "mark as paid" button in the admin would be a way to record money that never arrived. A test
asserts that no route under `/admin/orders` accepts a `POST`, so adding one is a decision
somebody has to make on purpose.

The order page shows the **snapshot** — the title, options and unit price as they were when
the order was placed — so renaming, repricing or withdrawing a product afterwards does not
rewrite what somebody bought. It also shows the raw gateway notification, for the day a
customer and a bank disagree about what happened.

The list is capped at the 200 most recent. Products are a small fixed set; orders accumulate
forever, so "the catalog is small" does not carry over to this table.

## What gets sent, and when

When an authenticated notification says an order is paid, two emails go out — and both go out
**after** the order is recorded paid. That ordering is the whole point: a mail server having a
bad afternoon must never be able to lose a sale. Nothing in the mail path can fail the payment
callback.

- **The customer** gets a receipt, once. `orders.emailed` records it, so a replayed gateway
  notification does not send a second copy. Both a plain-text and an HTML part are sent; the
  plain-text one is not optional, because a receipt has to arrive readable in a client that
  refuses HTML.
- **`ORDER_NOTIFY_EMAIL`**, if set, gets a work order: what to pack, where it goes, and a link
  to the admin page. It also carries the **oversell warning**, which otherwise exists only in
  the logs — the person who has to tell a customer their item is gone should not have to find
  that in a log aggregator.

They are two separate sends rather than one message with two recipients: a receipt and a work
order say different things, and one of them failing should not suppress the other.

**Mail is required and the server will not start without it.** That reverses what this
section used to say. The old reasoning — the shop's job is to take an order and record it,
which does not depend on a mail server — held while every product was a parcel. It stopped
holding when a product could be a download: that link is emailed and nowhere else, because
only its hash is stored. `mailer.Discard` still exists for tests and for an adopter assembling
their own `main`, but the shipped binary no longer reaches it.

Sending itself lives in [`github.com/17xande-dev/mailer`](https://github.com/17xande-dev/mailer),
which is shared with another application rather than kept here.

## Microsoft Exchange Online

Basic Auth for SMTP client submission is going away, so an Exchange mailbox is reached with
**XOAUTH2**: the password becomes an OAuth2 access token, fetched per send and cached until
shortly before it expires.

```bash
SMTP_HOST=smtp.office365.com
SMTP_PORT=587
SMTP_TLS=starttls
SMTP_USERNAME=orders@example.com   # the mailbox; XOAUTH2 authenticates as a named one
EMAIL_FROM=orders@example.com
SMTP_OAUTH_TENANT_ID=...
SMTP_OAUTH_CLIENT_ID=...
SMTP_OAUTH_CLIENT_SECRET=...
# and no SMTP_PASSWORD — setting both refuses to boot
```

**The tenant-side setup is the part that bites**, and none of it is visible from here: a
tenant that has not been set up hands out a token perfectly happily and the mail server then
refuses it. You need an Entra ID app registration with the **`SMTP.SendAsApp`** application
permission (Office 365 Exchange Online) and admin consent, a service principal for it
registered in Exchange Online, **Send As** on the mailbox, and **SMTP AUTH enabled for that
mailbox** — it is disabled tenant-wide by default. Check each against current Microsoft
documentation; these requirements move.

Worth weighing before choosing this at all: Exchange Online is a mailbox service rather than
a transactional relay, and it throttles accordingly. A dropped confirmation costs a buyer
their download link, since it exists nowhere else. A dedicated transactional provider over
ordinary SMTP is the lower-risk option for a storefront, and needs none of the above — just
`SMTP_USERNAME` and `SMTP_PASSWORD`.

**Graph is the alternative to the XOAUTH2 setup above**, not an addition to it — set
`GRAPH_TENANT_ID`/`GRAPH_CLIENT_ID`/`GRAPH_CLIENT_SECRET` and Graph is used instead of SMTP,
`SMTP_HOST` does not need to be set at all, and `EMAIL_FROM` still names the sending mailbox:

```bash
EMAIL_FROM=orders@example.com
GRAPH_TENANT_ID=...
GRAPH_CLIENT_ID=...
GRAPH_CLIENT_SECRET=...
```

It needs a **different** app permission than the SMTP path — **`Mail.Send`** (application,
admin-consented) for the `EMAIL_FROM` mailbox, not `SMTP.SendAsApp` — and no SMTP AUTH tenant
setting at all, which is the appeal: no service principal, no per-mailbox SMTP AUTH toggle.
The trade is on the wire, not in the setup: sends go as raw MIME to Graph's `sendMail`
endpoint rather than through SMTP, and **Bcc has no envelope on this transport** — it travels
as a real header instead, which is unverified against a live tenant. gostore never sets Bcc on
either order email today, so this does not affect it either way; it matters if that changes.

## Email templates

`mail/email_order_paid.txt`, `mail/email_order_paid.gohtml` and `mail/email_order_notify.txt`,
overridable from `TEMPLATE_DIR` like any other template. The `.txt` files go through
**`text/template`** and the rest through `html/template`, which is not a detail: running a
receipt through the HTML escaper puts `&amp;` in front of a customer.

They are the one thing under `TEMPLATE_DIR` that no layout wraps: a message is not a page,
and giving it the store's `<head>` and site nav would be actively wrong. They still see
`partials/`, so `{{template "csrf" …}}` would resolve in an email — which is meaningless
and harmless, and cheaper than a second partials set to prevent it.

The HTML part is deliberately primitive — table layout, inline styles, no external CSS and no
images. Mail clients are twenty years behind browsers, and a receipt that renders everywhere
beats one that looks better in three clients and breaks in the rest.

## Overselling

Two people can pay for the last item, because stock is only taken at payment. When a
decrement would go negative the order is still recorded paid — the money has been taken, and
refusing to record it would lose the sale *and* still be oversold — and the event is logged at
error level. Surfacing it in the admin's order view is part of the hardening phase.

## Money

Integer cents everywhere in Go and in the database; a decimal string only at the gateway
boundary and in rendered pages. A float total rounded differently from a gateway's amount
string is a real and hard-to-find class of bug, and the amount comparison in the callback is
exactly where it would bite.
