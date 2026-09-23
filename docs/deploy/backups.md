# Backups

Each deployment's `backup.sh`, run nightly from cron, leaves a compressed database dump in
`/opt/gostore/backups` — see step 9 of the [standard](standard.md#9-back-up-nightly) or
[tunnel](tunnel.md#9-back-up-nightly) guide. That protects against a bad migration or a
mistaken delete. It does not protect against losing the server: the dumps are on the same
disk as the database they copy.

This guide copies them, and the rest of what the store cannot rebuild, somewhere else,
restores from them, and checks that restoring works.

## What needs backing up

| What | Where it lives | Standard | Tunnel |
|---|---|---|---|
| The database — products, orders, accounts | Postgres's volume, dumped by `backup.sh` | copy off-box | copy off-box |
| Product images | the image bucket | already in R2 | MinIO's volume — copy off-box |
| Purchased files, with `DOWNLOAD_DIR` | the `gostore_downloads` volume | copy off-box | copy off-box |
| `.env` | `/opt/gostore/.env` | keep a copy in your password manager | same |

Everything else — the image, Caddy's certificates, the tunnel — is recreated by following
the guide again.

## Copying off the server

The copies go to a private R2 bucket, with [rclone](https://rclone.org) run from its
container, so nothing is installed on the host. Any S3-compatible storage works the same
way with a different endpoint.

**Create the bucket and a token.** In the Cloudflare dashboard, under **R2 Object
Storage**, create a bucket called `gostore-backups` and leave public access **off**. Then
**Manage API tokens** → **Create API token**, with **Object Read & Write** on only that
bucket, and copy the access key id, secret and endpoint. Use a token of its own, not the
image bucket's: a leaked store credential should not be able to delete the backups.

**Retention** belongs to the bucket: under its **Settings** → **Object lifecycle rules**,
add a rule deleting objects after, say, 90 days. The copy below never deletes anything, so
a dump pruned on the server stays off it until the rule removes it.

**Give rclone the credentials** in a file of their own beside `.env`, at `0600`:

```sh
sudo install -m 600 /dev/null /opt/gostore/offsite.env
sudo nano /opt/gostore/offsite.env
```

```sh
RCLONE_CONFIG_OFFSITE_TYPE=s3
RCLONE_CONFIG_OFFSITE_PROVIDER=Cloudflare
RCLONE_CONFIG_OFFSITE_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
RCLONE_CONFIG_OFFSITE_ACCESS_KEY_ID=
RCLONE_CONFIG_OFFSITE_SECRET_ACCESS_KEY=
# The token can write to the bucket but not create it, so rclone must not try.
RCLONE_CONFIG_OFFSITE_NO_CHECK_BUCKET=true
```

That defines an rclone remote called `offsite`, with no config file to keep.

**The copy script**, `/opt/gostore/offsite.sh`:

```bash
#!/usr/bin/env bash
# Copies what backup.sh and the store keep on this server to the offsite bucket.
set -euo pipefail
cd "$(dirname "$0")"

extra=()
rclone() {
  docker run --rm --network gostore_default --env-file offsite.env \
    -v "$PWD/backups:/backups:ro" -v gostore_downloads:/downloads:ro \
    "${extra[@]}" rclone/rclone:latest --quiet "$@"
}

rclone copy /backups offsite:gostore-backups/db
rclone copy /downloads offsite:gostore-backups/downloads
```

On the **tunnel** deployment, add product images, read straight out of MinIO — the
`minio` remote is defined inline, from `.env`'s password:

```bash
extra=(-e RCLONE_CONFIG_MINIO_TYPE=s3 -e RCLONE_CONFIG_MINIO_PROVIDER=Minio
       -e RCLONE_CONFIG_MINIO_ENDPOINT=http://minio:9000
       -e RCLONE_CONFIG_MINIO_ACCESS_KEY_ID=gostore
       -e "RCLONE_CONFIG_MINIO_SECRET_ACCESS_KEY=$(grep '^MINIO_ROOT_PASSWORD=' .env | cut -d= -f2-)")
rclone copy minio:gostore-images offsite:gostore-backups/images
```

Make it executable, run it once, and check the bucket in the dashboard:

```sh
sudo chmod 700 /opt/gostore/offsite.sh
sudo /opt/gostore/offsite.sh
```

Then have cron run it after each successful backup, replacing the guide's line in
`sudo crontab -e`:

```crontab
15 3 * * * /opt/gostore/backup.sh && /opt/gostore/offsite.sh >>/var/log/gostore-backup.log 2>&1
```

`gostore_default` and `gostore_downloads` are the names Compose gives the stack's network
and volume, from `name: gostore` at the top of `compose.yaml`.

## Restoring the database

On the server, in `/opt/gostore`. This **replaces** the current database with the dump,
so take one of the current state first, in case you want it back:

```sh
sudo ./backup.sh
sudo docker compose stop server
sudo docker compose exec -T postgres dropdb -U gostore gostore
sudo docker compose exec -T postgres createdb -U gostore gostore
gunzip -c backups/gostore-<timestamp>.sql.gz \
  | sudo docker compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U gostore gostore >/dev/null
sudo docker compose start server
```

To restore from the offsite copy — on a new server, after following the guide up to
starting the stack — fetch the dump first:

```sh
sudo docker run --rm --env-file offsite.env -v "$PWD/backups:/backups" \
  rclone/rclone:latest --quiet copy offsite:gostore-backups/db/gostore-<timestamp>.sql.gz /backups
```

Purchased files and, on the tunnel deployment, images come back the same way, with the
source and destination swapped: `copy offsite:gostore-backups/downloads /downloads` with
the volume mounted read-write, and `copy offsite:gostore-backups/images minio:gostore-images`.

## Testing a restore

A backup that has never been restored is a hope. Every so often — and after changing
anything above — restore the latest dump into a scratch database beside the real one, and
look at it:

```sh
sudo docker compose exec -T postgres createdb -U gostore restore_check
gunzip -c "$(ls -t backups/gostore-*.sql.gz | head -1)" \
  | sudo docker compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U gostore restore_check >/dev/null
sudo docker compose exec -T postgres psql -U gostore restore_check -c 'select count(*) from orders'
sudo docker compose exec -T postgres dropdb -U gostore restore_check
```

The live store is untouched throughout. Do the same with a dump fetched from the offsite
bucket now and then, since that is the copy you will need on the day the server is gone.

## What this does not give you

These are nightly snapshots: restoring loses whatever happened since the last one — up to
a day of orders. Point-in-time recovery needs Postgres's write-ahead log shipped
continuously, with a tool such as [pgBackRest](https://pgbackrest.org) or
[WAL-G](https://github.com/wal-g/wal-g), or a managed database that does it for you.
Either is a bigger step than this guide, and worth taking once a lost day of orders costs
more than running it.
