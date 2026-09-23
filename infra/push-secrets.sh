#!/usr/bin/env bash
# Push one environment's runtime secrets from pass to its VM, then (re)start the
# stack. Usually run as `make secrets ENV=staging` (or ENV=prod).
#
# This is the only way a secret reaches a box:
#
#   pass (GPG, on this machine) -> this script -> ssh stdin -> one 0400 file
#   per secret under /opt/gostore/secrets
#
# Never Terraform state, never cloud-init user-data, never a command line.
# Terraform decides WHICH secrets an environment needs (its secret_names
# output, derived from the features switched on); pass holds WHAT they are.
#
# Entries live at gostore/<env>/<name>, and the first line is the value — the
# pass convention, so an entry can carry notes on the lines below it. Two
# names do not come from pass: database_url is built from postgres_password,
# so the two can never disagree, and tunnel_token is read from Terraform,
# because Cloudflare generates it when the tunnel is created.
#
# HOST=user@address overrides the target, which a DHCP staging VM needs.
set -euo pipefail
shopt -s inherit_errexit

env=${1:-}
case "$env" in
  staging) dir=proxmox ;;
  prod)    dir=vultr ;;
  *)
    echo "usage: $0 staging|prod   (or: make secrets ENV=staging)" >&2
    exit 2
    ;;
esac

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tf=(terraform -chdir="$here/terraform/$dir")

output() {
  if ! "${tf[@]}" output -raw "$1" 2>/dev/null; then
    echo "no '$1' output in infra/terraform/$dir — run terraform apply there first" >&2
    return 1
  fi
}

app=$(output app_name)
names=$(output secret_names)
target=${HOST:-$(output ssh_target)}
if [ -z "$target" ]; then
  echo "no ssh_target (a DHCP VM's address is not known to Terraform); set HOST=user@address" >&2
  exit 1
fi

# The ones worth generating rather than choosing. -n keeps them to letters and
# digits: database_url embeds postgres_password in a URL, and backup.sh embeds
# the storage secret in one too, so a symbol would need escaping in both.
generated=" postgres_password setup_token minio_root_password "

# from_pass NAME sets REPLY to the first line of gostore/<env>/NAME. It sets a
# variable rather than printing because it is called more than once for
# postgres_password (itself, and inside database_url), and a cache written
# from inside $(...) would vanish with the subshell — decrypting twice, and
# reporting a missing entry twice.
declare -A cache missing
from_pass() {
  local entry="gostore/$env/$1" out
  if [ -n "${cache[$1]+set}" ]; then
    REPLY=${cache[$1]}
    return 0
  fi
  if [ -n "${missing[$1]+set}" ]; then
    return 1
  fi
  if ! out=$(pass show "$entry" 2>/dev/null); then
    missing[$1]=1
    case "$generated" in
      *" $1 "*) echo "  missing $entry — create it: pass generate -n $entry 40" >&2 ;;
      *)        echo "  missing $entry — create it: pass insert $entry" >&2 ;;
    esac
    return 1
  fi
  cache[$1]=${out%%$'\n'*}
  REPLY=${cache[$1]}
}

# Every value is gathered before the VM is touched. A missing entry is
# reported with the rest and nothing is pushed: half a set of new secrets
# on the box is worse than the old set, whole.
declare -A value
failed=0
for name in $names; do
  # Names come from Terraform and end up in a remote path, so they are held
  # to what a secret name actually looks like.
  if [[ ! $name =~ ^[a-z0-9_]+$ ]]; then
    echo "refusing unexpected secret name: $name" >&2
    exit 1
  fi
  case "$name" in
    database_url)
      if from_pass postgres_password; then
        value[$name]="postgres://$app:$REPLY@postgres:5432/$app?sslmode=disable"
      else
        failed=1
      fi
      ;;
    tunnel_token)
      value[$name]=$("${tf[@]}" output -raw tunnel_token)
      ;;
    *)
      if from_pass "$name"; then
        value[$name]=$REPLY
      else
        failed=1
      fi
      ;;
  esac
  if [ -n "${value[$name]+set}" ] && [ -z "${value[$name]}" ]; then
    echo "  gostore/$env/$name is empty" >&2
    failed=1
  fi
done
if [ "$failed" -ne 0 ]; then
  echo "nothing was pushed." >&2
  exit 1
fi

# One SSH connection for the whole run rather than one per secret.
ctl=$(mktemp -d)
ssh_opts=(-o ControlMaster=auto -o "ControlPath=$ctl/%C" -o ControlPersist=60)
cleanup() {
  ssh "${ssh_opts[@]}" -O exit "$target" 2>/dev/null || true
  rm -rf "$ctl"
}
trap cleanup EXIT
remote() { ssh "${ssh_opts[@]}" "$target" "$@"; }

echo "pushing $(wc -w <<<"$names") secrets from gostore/$env to $target"
remote 'sudo -n install -d -m 0700 -o root -g root /opt/gostore/secrets'
for name in $names; do
  # The value goes over stdin, never as an argument. Owned by 65532 —
  # distroless's nonroot, which the server and cloudflared run as — and
  # written beside the old file then moved over it, so a rotation never
  # leaves a half-written secret for a container to read.
  printf '%s' "${value[$name]}" | remote "sudo -n install -o 65532 -g 65532 -m 0400 /dev/stdin /opt/gostore/secrets/$name.new && sudo -n mv -f /opt/gostore/secrets/$name.new /opt/gostore/secrets/$name"
  echo "  $name"
done

# --force-recreate because a container holds the file it was started with: the
# mv above gives each secret a new inode, and the server reads its secrets
# once, at boot. That restarts the stack, Postgres included — seconds, on a
# rotation that happens rarely.
echo "starting the stack"
remote 'cd /opt/gostore && sudo -n docker compose up -d --force-recreate'

echo
echo "done. First admin account: /admin/setup with the token from"
echo "  pass show gostore/$env/setup_token"
