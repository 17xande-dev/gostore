# Deploying: standard

This guide takes you from an empty server to a running store: gostore and Postgres under
Docker Compose, Caddy in front with a Let's Encrypt certificate, product images in
Cloudflare R2, and mail through any SMTP provider. It uses
[`deploy/standard`](../../deploy/standard).

## 1. What you need

- **A server** with a public IPv4 address — any VPS running a current Ubuntu or Debian.
  One vCPU and 1 GB of memory is enough to start. You need SSH access and `sudo`.
- **A domain**, and a hostname on it for the store (say `shop.example.com`).
- **A Cloudflare account** for R2. The free tier covers a typical shop's images.
- **An SMTP provider** — the host, port, username and password it gives you, and an
  address it lets you send from. The server refuses to start without mail, because a
  digital download's link exists only in its confirmation email.
- **A published image.** Use a release from
  [GHCR](https://github.com/17xande-dev/gostore/pkgs/container/gostore), or your own; see
  [Publishing an image](README.md#publishing-an-image).

## 2. Prepare the server

Install Docker Engine and the Compose plugin by
[Docker's instructions for your distribution](https://docs.docker.com/engine/install/).
`docker compose version` should print a version.

Allow SSH, HTTP and HTTPS in, and nothing else. With `ufw`:

```sh
sudo ufw allow OpenSSH && sudo ufw allow 80/tcp && sudo ufw allow 443 && sudo ufw enable
```

Caddy is the only container that publishes a port; Postgres and the store are reachable
only from inside the stack.

## 3. Point the domain at the server

Create an **A record** for `shop.example.com` with the server's address. Caddy asks Let's
Encrypt for a certificate on first start, and that only succeeds once the name resolves to
this server — check with `dig +short shop.example.com`.

If the domain is on Cloudflare, make this record **DNS only** (grey cloud). Proxied, every
request would arrive from Cloudflare with the client's address in a header this
deployment does not trust; the [tunnel deployment](tunnel.md) is the one built for
Cloudflare in front.

## 4. Create the R2 bucket

In the Cloudflare dashboard, under **R2 Object Storage**:

1. **Create bucket** — call it `gostore-images`.
2. Open it → **Settings** → **Public access** → **Custom Domains** → **Connect domain**,
   and give it a hostname such as `images.example.com` (the domain must be on your
   Cloudflare account). That address is `BLOB_PUBLIC_BASE_URL`. The `r2.dev` address
   also works, but Cloudflare rate-limits it and says it is not for production.
3. Back on the R2 overview → **Manage API tokens** → **Create API token**, with **Object
   Read & Write** permission applied to **only this bucket**. It shows an access key id, a
   secret access key and an endpoint of the form
   `https://<account-id>.r2.cloudflarestorage.com` — copy all three now; the secret is
   shown once.

## 5. Copy the deployment to the server

Take the `deploy/standard` directory from the same release as the image you will run:

```sh
git clone --depth 1 --branch v1.0.0 https://github.com/17xande-dev/gostore.git /tmp/gostore
sudo cp -r /tmp/gostore/deploy/standard /opt/gostore
cd /opt/gostore
sudo cp .env.example .env && sudo chmod 600 .env
```

## 6. Fill in `.env`

Open `.env` (`sudo nano .env`). Each value is explained beside it; in short:

| Setting | Value |
|---|---|
| `GOSTORE_VERSION` | the release you are running, e.g. `v1.0.0` |
| `DOMAIN` | `shop.example.com` |
| `ACME_EMAIL` | where Let's Encrypt writes about a certificate it cannot renew |
| `STORE_NAME` | what shoppers see |
| `POSTGRES_PASSWORD` | the output of `openssl rand -hex 32` |
| `PAYFAST_*` | leave the sandbox values for now; see [Going live](#going-live) |
| `BLOB_ENDPOINT` | the R2 endpoint from step 4, **without** `https://` |
| `BLOB_ACCESS_KEY_ID`, `BLOB_SECRET_ACCESS_KEY` | the token from step 4 |
| `BLOB_PUBLIC_BASE_URL` | `https://images.example.com` |
| `SMTP_*`, `EMAIL_FROM` | from your mail provider |

## 7. Start it

```sh
sudo docker compose up -d
sudo docker compose ps          # postgres healthy; server and caddy up
curl https://shop.example.com/healthz     # -> ok
```

The first start takes a minute: the images download, migrations run, and Caddy obtains
its certificate. If `/healthz` does not answer, `sudo docker compose logs server` names
any setting the server refused, and `sudo docker compose logs caddy` says why a
certificate could not be issued.

## 8. Claim the admin account

With no account yet, the server printed a one-time setup token when it started:

```sh
sudo docker compose logs server | grep setup_token
```

Open `https://shop.example.com/admin`, which redirects to `/admin/setup`, paste the token,
and choose an address and password. That account is the `owner`, and the token is spent.
See [Admin and accounts](../admin.md) for the rest.

## 9. Back up nightly

`backup.sh` writes a compressed dump of the database to `/opt/gostore/backups`, and keeps
14 days of them (`KEEP_DAYS` changes that). Run it once to see it work, then schedule it
in root's crontab (`sudo crontab -e`):

```sh
sudo /opt/gostore/backup.sh
```

```crontab
15 3 * * * /opt/gostore/backup.sh >>/var/log/gostore-backup.log 2>&1
```

It refuses to keep a dump that did not finish, and prunes old ones only after a good one.
These copies live on the same disk as the database; [Backups](backups.md) copies them
somewhere else and covers restoring.

## Day two

**Updating.** Take a backup, set `GOSTORE_VERSION` in `.env` to the new release, then:

```sh
sudo ./backup.sh
sudo docker compose pull && sudo docker compose up -d
```

Migrations run as the new version starts. Read the release notes first: a release that
changes this directory's files says so, and you copy those over as in step 5.

**Changing a setting or a secret.** Edit `.env` and run `sudo docker compose up -d`;
containers whose settings changed are recreated. `POSTGRES_PASSWORD` is the exception:
Postgres reads it only when the database is first created, so change the database first,
then `.env`:

```sh
sudo docker compose exec postgres psql -U gostore -c "ALTER USER gostore PASSWORD '<new>'"
```

## Going live

The store starts on PayFast's sandbox, which takes no real money. When you are ready,
replace `PAYFAST_MERCHANT_ID`, `PAYFAST_MERCHANT_KEY` and `PAYFAST_PASSPHRASE` with your
live values, set `PAYFAST_SANDBOX=false`, and `sudo docker compose up -d`. See
[Going live](../payments.md#going-live) for what to check before and after.

## When a step fails

| Symptom | Likely cause |
|---|---|
| `docker compose` says a variable is not set | That setting is empty in `.env`; the message names it |
| The server container keeps restarting | It refused a setting — `sudo docker compose logs server` names it |
| No certificate; the browser warns | DNS does not point here yet, the record is proxied through Cloudflare, or 80/443 are blocked |
| Images upload but do not display | `BLOB_PUBLIC_BASE_URL` is not the bucket's public address, or public access is off |
| `pull access denied` for the image | The GHCR package is private, or `GOSTORE_VERSION` names a tag that does not exist |
