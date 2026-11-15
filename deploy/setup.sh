#!/usr/bin/env bash
# textbridge server setup for a Debian/Ubuntu VM (built for Google Cloud's
# free e2-micro). Run it in the VM's SSH window:
#
#   curl -fsSL https://raw.githubusercontent.com/kaibrownco/textbridge/main/deploy/setup.sh | sudo bash
#
# Safe to run again: it updates the programs and keeps your pairing,
# passphrase and Claude sign-ins.
set -euo pipefail

REPO="${TB_REPO:-kaibrownco/textbridge}"
RAW="https://raw.githubusercontent.com/${REPO}/main"
REL="https://github.com/${REPO}/releases/latest/download"
BIN=/opt/textbridge/bin
DATA=/var/lib/textbridge
ETC=/etc/textbridge

say()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die()  { printf '\n\033[1;31mSetup stopped: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run with sudo (… | sudo bash)"
[ "$(uname -m)" = "x86_64" ] || die "this server must be x86_64 (pick an e2-micro machine)"
command -v apt-get >/dev/null || die "use a Debian or Ubuntu image"

say "Installing basics"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates gnupg python3 debian-keyring debian-archive-keyring apt-transport-https >/dev/null

say "Adding 1 GB of swap (the free server only has 1 GB of memory)"
if ! swapon --show | grep -q /swapfile; then
  fallocate -l 1G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

say "Creating the textbridge account and folders"
id textbridge >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin textbridge
install -d -m 0750 -o textbridge -g textbridge "$DATA" "$DATA/openmessage" "$DATA/gateway"
install -d -m 0755 "$BIN" "$ETC"

say "Downloading textbridge"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/tb.tar.gz" "$REL/textbridge-linux-amd64.tar.gz" \
  || die "couldn't download the release. Check https://github.com/${REPO}/actions finished, then run this again."
curl -fsSL -o "$tmp/tb.sha256" "$REL/textbridge-linux-amd64.tar.gz.sha256"
(cd "$tmp" && sed 's/  .*/  tb.tar.gz/' tb.sha256 | sha256sum -c --quiet) || die "download checksum didn't match"
tar -xzf "$tmp/tb.tar.gz" -C "$tmp"
install -m 0755 "$tmp/openmessage" "$tmp/textbridge-gateway" "$BIN/"
for f in textbridge-openmessage.service textbridge-gateway.service; do
  curl -fsSL -o "/etc/systemd/system/$f" "$RAW/deploy/systemd/$f"
done
curl -fsSL -o "$BIN/.rotate.new" "$RAW/deploy/rotate-google-cookies"
chmod 0755 "$BIN/.rotate.new" && mv -f "$BIN/.rotate.new" "$BIN/rotate-google-cookies"
# Write to a temp file and rename, so a running "textbridge update" (which is
# this very script's caller) keeps reading its old copy undisturbed.
curl -fsSL -o /usr/local/bin/.textbridge.new "$RAW/deploy/textbridge"
chmod 0755 /usr/local/bin/.textbridge.new
mv -f /usr/local/bin/.textbridge.new /usr/local/bin/textbridge

say "Working out this server's web address"
if [ -z "${TB_DOMAIN:-}" ]; then
  ip="$(curl -fsS -H 'Metadata-Flavor: Google' \
    http://169.254.169.254/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(curl -fsS https://api.ipify.org)"
  [ -n "$ip" ] || die "couldn't find this server's public IP"
  TB_DOMAIN="${ip//./-}.sslip.io"
fi
PUBLIC_URL="https://${TB_DOMAIN}"
echo "Address: $PUBLIC_URL"

cat > "$ETC/openmessage.env" <<EOF
HOME=$DATA/openmessage
OPENMESSAGES_DATA_DIR=$DATA/openmessage
OPENMESSAGES_HOST=127.0.0.1
OPENMESSAGES_PORT=7007
OPENMESSAGES_EXPORT_DIR=$DATA/openmessage/exports
OPENMESSAGES_MACOS_NOTIFICATIONS=0
OPENMESSAGE_COOKIE_REFRESH_SCRIPT=$BIN/rotate-google-cookies
EOF
cat > "$ETC/gateway.env" <<EOF
TB_PUBLIC_URL=$PUBLIC_URL
TB_LISTEN=127.0.0.1:8080
TB_UPSTREAM=http://127.0.0.1:7007
TB_STATE_DIR=$DATA/gateway
EOF
echo "$TB_DOMAIN" > "$ETC/domain"
chmod 0640 "$ETC"/*.env && chgrp textbridge "$ETC"/*.env

say "Installing Caddy (handles the secure https:// address)"
if ! command -v caddy >/dev/null; then
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy >/dev/null
fi
cat > /etc/caddy/Caddyfile <<EOF
${TB_DOMAIN} {
	encode gzip
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
	}
}
EOF

NEWPASS=""
if [ ! -s "$DATA/gateway/password" ]; then
  # gcp-create.sh passes a ready-made hash in instance metadata, so the
  # passphrase itself never touches this server or its logs.
  mdhash="$(curl -fsS -H 'Metadata-Flavor: Google' \
    http://169.254.169.254/computeMetadata/v1/instance/attributes/tb-password-hash 2>/dev/null || true)"
  if [[ "$mdhash" == pbkdf2-sha256\$* ]]; then
    say "Using the passphrase you were shown in Cloud Shell"
    printf '%s\n' "$mdhash" > "$DATA/gateway/password"
    chown textbridge:textbridge "$DATA/gateway/password"; chmod 600 "$DATA/gateway/password"
  else
    say "Creating your passphrase"
    NEWPASS="$(sudo -u textbridge env TB_STATE_DIR="$DATA/gateway" "$BIN/textbridge-gateway" new-password)"
  fi
fi

say "Starting everything"
systemctl daemon-reload
systemctl enable --quiet textbridge-openmessage textbridge-gateway caddy
systemctl restart textbridge-openmessage textbridge-gateway caddy

say "Checking the secure address (this can take a minute the first time)"
ok=""
for _ in $(seq 1 30); do
  if curl -fsS --max-time 5 "$PUBLIC_URL/healthz" >/dev/null 2>&1; then ok=1; break; fi
  sleep 4
done
if [ -z "$ok" ]; then
  echo "The https address isn't answering yet. Most likely the VM's firewall blocks it:"
  echo "in Google Cloud, edit the VM and tick 'Allow HTTP traffic' and 'Allow HTTPS traffic', then run: sudo textbridge restart"
fi

printf '\n\033[1;32m%s\033[0m\n' "textbridge is installed."
echo
echo "  Connector URL (paste into Claude):  $PUBLIC_URL/mcp"
if [ -n "$NEWPASS" ]; then
  echo
  echo "  Your passphrase (save it in your password manager NOW; it won't be shown again):"
  printf '\n      \033[1m%s\033[0m\n' "$NEWPASS"
fi
if [ -n "$NEWPASS" ]; then
  echo
  echo "  Next: link your phone with   sudo textbridge pair"
fi
echo
