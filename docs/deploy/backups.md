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
| Product images | public R2 image bucket | independent copy | independent copy |
| Purchased files | private R2 download bucket | independent copy | independent copy |
| `.env`, including `EMAIL_QUEUE_KEY` | `/opt/gostore/.env` | keep a copy in your password manager | same |

R2 is primary storage, not a backup against accidental deletion. Keep independent
copies under credentials the running store cannot use. Database backups include
encrypted pending emails: retain their matching `EMAIL_QUEUE_KEY` separately.

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

**Retention** belongs to the bucket: add a lifecycle rule for the **`db/` prefix**
deleting old dumps after, say, 90 days. Do not apply it to `images/` or `downloads/`:
old objects there may still be referenced by current orders. The copy never deletes
objects merely because the live bucket no longer contains them.

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

# A separate read-only token scoped to the two live buckets.
RCLONE_CONFIG_LIVE_TYPE=s3
RCLONE_CONFIG_LIVE_PROVIDER=Cloudflare
RCLONE_CONFIG_LIVE_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
RCLONE_CONFIG_LIVE_ACCESS_KEY_ID=
RCLONE_CONFIG_LIVE_SECRET_ACCESS_KEY=
RCLONE_CONFIG_LIVE_NO_CHECK_BUCKET=true
```

That defines `offsite` and `live` remotes. An independent account or provider for
`offsite` also protects against losing access to the primary account.

**The copy script**, `/opt/gostore/offsite.sh`:

```bash
#!/usr/bin/env bash
# Copies what backup.sh and the store keep on this server to the offsite bucket.
set -euo pipefail
cd "$(dirname "$0")"

rclone() {
  docker run --rm --env-file offsite.env \
    -v "$PWD/backups:/backups:ro" rclone/rclone:latest --quiet "$@"
}

rclone copy /backups offsite:gostore-backups/db
rclone copy live:gostore-images offsite:gostore-backups/images
rclone copy live:gostore-downloads offsite:gostore-backups/downloads
```

Use the actual bucket names from `BLOB_BUCKET` and `DOWNLOAD_BUCKET`. Both
production deployments use this same procedure.

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

For a custom disk-backed deployment, also mount its image/download directories
read-only into the backup container and copy their contents with the same key paths.

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

Restore images and purchased files by reversing the corresponding copy, using a
temporary write-capable credential for the destination buckets. For example:
`copy offsite:gostore-backups/downloads live:gostore-downloads`. Keep the download
bucket private. Restore `.env` and the matching queue key before starting the app;
pending emails in the restored snapshot may be delivered again.

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
