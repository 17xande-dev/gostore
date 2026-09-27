# Deploying: tunnel

This guide takes you from an empty server to a running store reached through a Cloudflare
Tunnel: gostore and Postgres under Docker Compose, `cloudflared` carrying requests
in, R2 for images and downloads, and mail through Microsoft Graph. It uses
[`deploy/tunnel`](../../deploy/tunnel).

`cloudflared` dials out to Cloudflare, so the server publishes **no ports at all** — no
port forward, no public address, no certificate to renew. That makes it the deployment
for a machine behind a home or office router, and a sound one for a VPS you would rather
keep closed.

## 1. What you need

- **A server** running a current Ubuntu or Debian, with outbound internet access. A VM
  on a home server is fine. One vCPU and 2 GB of memory is enough to start.
  You need SSH access and `sudo`.
- **A domain on Cloudflare**, and two hostnames on it: one for the store (say
  `shop.example.com`) and one for product images (`images.example.com`).
- **Microsoft 365**, and permission to register an app in its Entra tenant — or an admin
  who will. Mail is required for receipts and durable download links.
- **Two R2 buckets**, one public for images, one private for downloads. Follow
  [the R2 setup](standard.md#4-create-the-r2-buckets), including the separate tokens.
- **A published image.** Use a release from
  [GHCR](https://github.com/17xande-dev/gostore/pkgs/container/gostore), or your own; see
  [Publishing an image](README.md#publishing-an-image).

## 2. Prepare the server

Install Docker Engine and the Compose plugin by
[Docker's instructions for your distribution](https://docs.docker.com/engine/install/).
`docker compose version` should print a version. Nothing needs opening in a firewall; if
the server has one, it can refuse every inbound connection except your SSH.

## 3. Register the mail app

In the Entra admin center, register an application for the store and give it the
**`Mail.Send` application permission** on Microsoft Graph, with admin consent, then
create a client secret. [Microsoft Exchange Online](../email.md#microsoft-exchange-online)
covers what the store needs from it. On its own, `Mail.Send` lets the app send as any
mailbox in the tenant, so restrict it to the one it sends as — Exchange Online's RBAC for
Applications does that; check Microsoft's current documentation for the steps.

Keep three values: the **tenant id**, the app's **client id**, and the **client
secret** — shown once.

## 4. Create the tunnel

In the Cloudflare dashboard, open **Zero Trust** → **Networks** → **Tunnels** →
**Create a tunnel**:

1. Choose **Cloudflared**, and name it (say `gostore`).
2. On the install page, pick **Docker**. The command shown ends in `--token eyJ…` —
   copy just that token. It is `TUNNEL_TOKEN`, and it is the tunnel's whole credential.
   Do not run the command; the stack runs `cloudflared` itself.
3. Add the store's **public hostname** (newer dashboards call it a *published application
   route*):

   | Hostname | Service |
   |---|---|
   | `shop.example.com` | `HTTP` → `server:8080` |

   `server` is the container name inside the stack. Images use the custom domain
   connected directly to the public R2 bucket, not a tunnel route. Cloudflare creates
   the store's DNS record; an existing conflicting record must be removed first.

## 5. Copy the deployment to the server

Take the `deploy/tunnel` directory from the same release as the image you will run:

```sh
git clone --depth 1 --branch v1.0.0 https://github.com/17xande-dev/gostore.git /tmp/gostore
sudo cp -r /tmp/gostore/deploy/tunnel /opt/gostore
cd /opt/gostore
sudo cp .env.example .env && sudo chmod 600 .env
```

## 6. Fill in `.env`

Open `.env` (`sudo nano .env`). Each value is explained beside it; in short:

| Setting | Value |
|---|---|
| `GOSTORE_VERSION` | the release you are running, e.g. `v1.0.0` |
| `TUNNEL_TOKEN` | the token from step 4 |
| `DOMAIN` | the store hostname from step 4 |
| `STORE_NAME` | what shoppers see |
| `POSTGRES_PASSWORD`, `EMAIL_QUEUE_KEY` | the output of `openssl rand -hex 32`, once for each |
| `BLOB_*`, `DOWNLOAD_*` | the separate public/private R2 buckets and their tokens |
| `PAYFAST_*` | leave the sandbox values for now; see [Going live](#going-live) |
| `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `GRAPH_CLIENT_SECRET` | from step 3 |
| `EMAIL_FROM` | the mailbox the app sends as |

## 7. Start it

```sh
sudo docker compose up -d
sudo docker compose ps -a       # postgres healthy, server and cloudflared up
curl https://shop.example.com/healthz     # -> ok
```

The tunnel's page in the dashboard should now show it **Healthy**. If `/healthz` does not
answer, `sudo docker compose logs server` names any setting the server refused, and
`sudo docker compose logs cloudflared` shows whether the tunnel connected.

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
Images and downloads are not in the dump — they are in R2.
[Backups](backups.md) covers independent copies and restoring.

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

A new Graph client secret — they expire, two years at most — is an ordinary change:
create it in Entra, put it in `.env`, `up -d`.

**Older MinIO installations.** Follow the [storage migration notes](README.md#upgrading-from-local-production-storage)
before changing Compose files. Existing objects must be copied by key; the new
configuration does not move them automatically.

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
| The tunnel shows *Down* or *Inactive* | `TUNNEL_TOKEN` is wrong or truncated, or outbound traffic is blocked — see `logs cloudflared` |
| Cloudflare shows error 502 | The store hostname's service must be `server:8080`, over `HTTP` |
| Images upload but do not display | The R2 public domain or `BLOB_PUBLIC_BASE_URL` is incorrect |
| Mail fails with an authorization error | `Mail.Send` is missing admin consent, or the app is not allowed that mailbox |
