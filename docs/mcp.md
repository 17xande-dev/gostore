# MCP

gostore's admin is also a [Model Context Protocol](https://modelcontextprotocol.io) server, at
`/mcp`. Connect an AI assistant to it — Claude Code, Claude Desktop, or any MCP client — and it
can read and manage the store as you: list and edit products and their variants, organise
categories, look up orders and mark them shipped.

It is a second door onto the same rooms. The assistant holds an API token that acts as your
account, it can do exactly what your role can, and every change goes through the same rules the
HTML admin applies.

## Connecting a client

**Make a token.** Tokens are for `owner` and `admin` accounts. In the admin, open the profile
icon at the right of the header, then **Profile settings** (`/admin/account`); in **API tokens**,
name the token for where it will be used — "Claude Code, work laptop" — pick how long it lives,
and create it. It is shown once; copy it then.

**Claude Code:**

```sh
claude mcp add --transport http gostore https://shop.example.com/mcp \
  --header "Authorization: Bearer gst_…"
```

The profile page prints this command with your store's address and the token filled in.

**Any other client** needs three things: the Streamable HTTP transport, the URL
`https://<your store>/mcp`, and the header `Authorization: Bearer gst_…`. Clients that only
speak stdio can reach it through a bridge such as `mcp-remote`.

Then ask for things in plain language — "which orders are paid but not shipped?", "add a Large
variant to the blue mug at 149.99 with 10 in stock", "mark order 3f2a… shipped with tracking
TRK123".

## Tools

| Tool | Needs | Does |
|---|---|---|
| `list_products` | read | Every product with its variants |
| `get_product` | read | One product: variants, categories, image URL, files, and its admin page |
| `list_categories` | read | Every category, in display order |
| `search_orders` | read | Orders newest first, 50 a page; text search and the `oversold` / `email` / `unfulfilled` filters |
| `get_order` | read | One order: items, customer, payment facts, fulfilment, download entitlements, email delivery |
| `create_product` | catalog.write | A new product; a blank slug is derived from the title |
| `update_product` | catalog.write | Changes only the fields given |
| `delete_product` | catalog.write | Refused once a product has been ordered — deactivate it instead |
| `create_variant` / `update_variant` | catalog.write | SKU, option values, price, stock, active |
| `delete_variant` | catalog.write | Refused once ordered |
| `create_category` / `update_category` | catalog.write | |
| `delete_category` | catalog.write | Removes it from its products; the products are untouched |
| `set_fulfillment` | orders.write | Shipped or not, tracking reference, internal note — paid orders with physical items only |
| `retry_order_email` | orders.write | Sends an order's undelivered emails again |
| `revoke_entitlement` / `restore_entitlement` | orders.write | Stops, or restores, a buyer's download link |

"Needs" is the permission from [Roles](admin.md#roles). Only `owner` and `admin` accounts may
hold a token, and both hold every permission, so today a token can call every tool; the
per-tool check stays so that a role added later with tokens but fewer permissions is limited
by it. Tools are annotated read-only or destructive, so a
client that asks before destructive actions will ask before a delete.

Prices go in and come out as decimal strings in the store's currency — `"149.99"` — never as
floating-point numbers.

**Deliberately missing:**

- **Uploading images and downloadable files.** File bytes do not belong in a tool call, a digital
  file can be gigabytes, and "fetch the image from this URL" would make the store fetch whatever
  it is told to — and break the rule that an image is always bytes the store holds.
  `get_product` returns the product's admin page, which is where uploads happen.
- **Administrator accounts.** Creating accounts, changing roles and resetting passwords stay in
  the browser. An assistant that could create an owner would be a way to escalate from any
  token to every permission.
- **Payment state.** Nothing can mark an order paid or refunded — the HTML admin cannot either.

## Security model

- **A token is its account acting.** It carries no role or scope of its own; the account's role
  at the moment of each call decides what it may do. Changing that account's password or role,
  or disabling it, revokes every token it holds at once, in the same transaction as its browser
  sessions. A disabled account, or one that must change its password, gets no access at all —
nor does a `manager` or `viewer` holding a token made before only administrators could.
- **Tokens expire** — after 30, 90 or 365 days; there is no "never" — and can be revoked
  individually from your profile settings. Only the token's `sha256` is stored, as for sessions, so a
  database backup holds no usable token. Each starts `gst_`, so a leaked one is recognisable to
  a person or a secret scanner.
- **Every call authenticates afresh.** The endpoint is stateless: there is no MCP session to
  hijack, and a revoked token is refused on its very next call.
- **Rate limited** per client IP, before the token is checked — `RATE_LIMIT_MCP_PER_MINUTE`,
  default 120. See [Rate limits](security.md#rate-limits).
- **Logged.** Every write is logged with the account and the token that made it, so "which
  assistant changed this?" has an answer. Order fulfilment also records the account on the order,
  exactly as the admin page does.
- **No CSRF, on purpose.** The endpoint takes no cookies, so a browser cannot be tricked into
  calling it with your credentials; the bearer token is the whole credential.

What an assistant does with the store is still what you asked it to do: a token can change
anything its administrator can. Give each client its own token, and revoke tokens you are no
longer using.
