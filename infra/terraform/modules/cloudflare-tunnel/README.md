# cloudflare-tunnel module

Creates a Cloudflare Tunnel, its ingress rules, and a DNS record per hostname,
and hands back the connector token. The root module re-exports that token as
its `tunnel_token` output, and `make secrets` writes it to the box as a secret
file, which [`../app-stack`](../app-stack/README.md) mounts into `cloudflared`
as `TUNNEL_TOKEN_FILE`. It is the one secret Terraform does hold — Cloudflare
gives it to Terraform when the tunnel is created — but it still never travels
in cloud-init. Used by a root module when it sets `ingress = "tunnel"`; a
`caddy` deployment never instantiates it and never needs a Cloudflare
credential.

## What it creates

- **`cloudflare_zero_trust_tunnel_cloudflared`** with `config_src =
  "cloudflare"`. That is the load-bearing argument: it puts the ingress rules
  in Cloudflare's API, where the config resource below writes them and the
  connector pulls them down. The alternative, `"local"`, expects a `config.yml`
  on the box and ignores anything set through the API — which would move the
  routing back into a file nobody reviews.
- **`data.cloudflare_zero_trust_tunnel_cloudflared_token`** — the connector
  token. A data source rather than an attribute, because the v5 provider does
  not expose one on the tunnel resource.
- **`cloudflare_zero_trust_tunnel_cloudflared_config`** — the ingress rules,
  one per `routes` entry, plus a catch-all the module appends itself. The
  catch-all answers `http_status:404` rather than a service, so a hostname
  pointed here by mistake says so instead of quietly reaching the store.
- **`cloudflare_dns_record`** — one proxied CNAME per hostname, pointing at
  `<tunnel-id>.cfargotunnel.com`.

## Two values that are not preferences

`proxied = true`, because `*.cfargotunnel.com` does not resolve publicly —
only the orange-cloud path reaches a tunnel at all. And `ttl = 1`
("automatic"), because the API refuses any other TTL on a proxied record.

## Service addresses are compose service names

`routes[*].service` is `http://server:8080`, not an IP or a published port.
`cloudflared` runs on the stack's own compose network, so Docker's embedded
DNS resolves it exactly as Caddy's `reverse_proxy` target did — which is why
the tunnel deployment can publish no host ports at all.

## Destroying

Two things to know, both of which will otherwise be discovered at the worst
moment:

- **Terraform cannot delete a tunnel that still has a connector attached.**
  Run `docker compose down` on the box before `terraform destroy`, or the
  destroy fails partway with the tunnel still present.
- **The config resource cannot be destroyed at all** — Terraform says so at
  plan time. It stays in the API until the tunnel it belongs to is deleted,
  which does take it with it, so in practice this matters only if you remove
  the config resource while keeping the tunnel.

## Credentials

The provider reads `CLOUDFLARE_API_TOKEN` from the environment; the root
module's `versions.tf` says why it is not a variable. Scope the token to what
this module actually does — Account: *Cloudflare Tunnel: Edit* and Zone:
*DNS: Edit* on the one zone — rather than using a Global API Key, which can
do anything to every zone on the account.

`account_id` and `zone_id` are identifiers rather than secrets, so they belong
in `terraform.tfvars` alongside `proxmox_node`.
