# Admin and accounts

## Admin

The admin lives at `/admin`. Accounts are rows in `admin_users`, not a credential in the
environment: there is no `ADMIN_PASSWORD_HASH` and no default password to forget to change.

**First run.** With no administrator in the database the server generates a one-time setup
token and prints it:

```sh
docker compose logs server | grep setup_token
```

`/admin/setup` exchanges that token for the first account, which is an `owner`, and
`/admin/login` redirects there while no account exists. The token is spent by the claim and
the page stops existing — permanently: the consumed timestamp is never cleared, so a restart
does not reopen it and neither does disabling every account. A deploy with nobody reading
logs can supply `SETUP_TOKEN` instead, in which case nothing is printed; the Terraform stack
generates one into Secret Manager and reads it with
`gcloud secrets versions access latest --secret=gostore-setup-token`.

The alternatives are both worse, and both common. A fixed default credential is a CVE class,
especially in a project published for others to copy. An unguarded first-run wizard is a race
that whoever finds the deployment first — before its operator does — wins.

**Sessions are rows, not signed cookies.** A sign-in inserts into `admin_sessions` and the
cookie carries 32 bytes from `crypto/rand`. Only `sha256(token)` is stored, so a leaked
backup hands over no live session, and there is no signing secret to configure or rotate.

The row is what makes a session revocable, which is the whole reason for the table: changing
an account's password, disabling it, or changing its role deletes every session it holds in
the same transaction, so the next request from that browser is anonymous rather than valid
until the cookie happens to expire. Expiry is enforced in the lookup's own SQL predicate; the
hourly sweep of expired rows keeps the table bounded and is explicitly not what makes it
correct.

The costs, stated plainly: one indexed lookup per admin request, and a session that outlives
a `DELETE FROM admin_sessions` does not exist. Both are the price of being able to end one.

If nobody can sign in at all — every owner disabled, or the only password lost — `make hashpw`
prints a hash to set by hand. See [`cmd/hashpw`](../cmd/hashpw/main.go) for the `UPDATE`, and for
the `DELETE FROM admin_sessions` that has to go with it.

Notes:

- The cookie is `HttpOnly`, `SameSite=Lax`, scoped to `/admin`, and `Secure` whenever
  `BASE_URL` is `https://` — so it is never sent with the cookie-free embeddable catalog
  fragments.
- Signing in replaces whatever session the browser arrived with, rather than adopting it.
  That is session fixation: an attacker who can set a cookie plants a token and waits.
- A redirect back to where you were going (`?next=`) is reduced by an allowlist — a path
  under `/admin/`, no `//`, no CR/LF, no `..` — so it cannot send anybody off the site.
- A session lookup that *fails* is a `500`, not a redirect to the login form. Answering
  "please sign in" during a database outage sends an operator round a loop that cannot
  complete, with the outage reported as an authentication problem.
- htmx requests that have lost their session get `401` and `HX-Refresh: true` instead of a
  redirect, because swapping a login page into a fragment produces a broken hybrid.
- Login, the setup claim and the change-your-own-password form share one rate limit of 10
  attempts a minute per IP; see [Hardening](security.md#hardening). All three verify a secret, and each
  one spends 64 MiB on argon2 doing it.
- What each account may actually *do* is [Roles](#roles), below.

## Roles

Four roles, following Stripe's dashboard split reduced to the surface this store has. Read
access to the catalog and the orders comes with having a session at all, so the table is
really about who may write:

| Role | Catalog | Orders & entitlements | Accounts | |
|---|---|---|---|---|
| `owner` | write | write | write | Cannot be disabled or demoted while it is the last enabled owner |
| `admin` | write | write | write | |
| `manager` | write | write | — | The shop-runner role |
| `viewer` | read | read | — | Looks, changes nothing |

`owner` and `admin` are identical in capability. `owner` exists to be the account the
last-account guard protects, which makes "who can never be locked out" a visible fact about
a row rather than something that emerges from counting.

**Permissions are a static map in Go** — `auth.Role` to a set of `auth.Permission` — not a
table. A permissions table would put a join on every request to buy configurability nobody
asked for, and would let a deployment invent a role the code has never heard of.

**Every route names the permission it needs on the line that registers it**, so
authorization travels with the route the way authentication already does:

```go
admin("GET  /admin/products", auth.PermRead,         h.adminProductList)
admin("POST /admin/products", auth.PermCatalogWrite, h.adminProductCreate)
admin("POST /admin/users",    auth.PermUsersWrite,   h.adminUserCreate)
```

The registration records the pair, so `AdminProtectedRoutes()` is what the tests sweep
rather than a list maintained beside the real one. Templates ask `.Can "catalog.write"` and
leave out what a role could not use — a button that is merely absent is not a restriction on
anybody who types the address, so that is presentation and `requirePerm` is the enforcement.

### Managing accounts

`/admin/users` is `users.write` only. What it will not do is as much of the design as what
it will:

- **Accounts are disabled, never deleted.** A removed row erases who did what, and the
  products and orders an administrator touched outlive their employment. Disabling ends
  their sessions in the same transaction.
- **Nobody may change their own role, disable themselves, or reset their own password from
  these pages.** A reset skips the current-password check, so allowing it against yourself
  would make an unattended screen enough to take an account over for good; an administrator
  who can change their own role is not held by it. All three answer `409`, and the controls
  are absent rather than present-and-refusing.
- **The last owner who can still sign in cannot be disabled or demoted**, from either
  direction — both guards take the same advisory lock, because otherwise two administrators
  removing two different owners each pass a count nobody re-reads.
- **A password somebody else chose is temporary.** Creating an account, or resetting its
  password, sets `must_change_password`; every route except the change form itself then
  bounces there until a new one is chosen.
- **Changing your own password asks for the current one.** A CSRF token proves the request
  came from our form, not that the person at the keyboard owns the account. It ends every
  session including the one doing it, and lands on the login form.

### A first run, end to end

```sh
make up
docker compose logs server | grep setup_token
```

1. Open `/admin` — with no account it redirects to `/admin/setup`.
2. Paste the token, choose an address and a password: that account is the `owner`.
3. `/admin/users/new` creates the rest. Give each one the least role that covers their job;
   `manager` is the usual answer for somebody running the shop.
4. Hand over the starting password however you like. They will be asked to replace it before
   any other admin page opens.
5. When somebody leaves, disable them. Their sessions stop on their next request, and the
   record of what they did stays.
