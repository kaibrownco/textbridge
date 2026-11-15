#!/usr/bin/env bash
# One-paste textbridge setup from Google Cloud Shell.
#
#   curl -fsSL https://raw.githubusercontent.com/kaibrownco/textbridge/main/deploy/gcp-create.sh | bash
# Creates a free e2-micro server (us-west1), opens https, sets a $1 budget
# alert, installs textbridge on it, and prints your connector URL and
# passphrase. Safe to run again.
set -euo pipefail

REPO="${TB_REPO:-kaibrownco/textbridge}"
NAME="${TB_VM:-textbridge}"
ZONE="${TB_ZONE:-us-west1-b}"

say() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mStopped: %s\033[0m\n' "$*" >&2; exit 1; }

command -v gcloud >/dev/null || die "run this in Google Cloud Shell"

say "Picking your project"
PROJECT="$(gcloud config get-value project 2>/dev/null || true)"
if [ -z "$PROJECT" ] || [ "$PROJECT" = "(unset)" ]; then
  PROJECT="$(gcloud projects list --format='value(projectId)' --limit=1)"
  [ -n "$PROJECT" ] || die "no Google Cloud project found; create one in the console first"
  gcloud config set project "$PROJECT" >/dev/null
fi
echo "Project: $PROJECT"
BILLING="$(gcloud billing projects describe "$PROJECT" --format='value(billingAccountName)' 2>/dev/null || true)"
[ -n "$BILLING" ] || die "this project has no billing account linked. In the console: Billing > Link a billing account, then run this again."

say "Turning on the Compute Engine service (first time takes a minute)"
gcloud services enable compute.googleapis.com billingbudgets.googleapis.com --quiet

say "Setting a \$1 budget alert"
if gcloud billing budgets list --billing-account="${BILLING#billingAccounts/}" --format='value(displayName)' 2>/dev/null | grep -qx textbridge; then
  echo "Already set."
else
  gcloud billing budgets create --billing-account="${BILLING#billingAccounts/}" \
    --display-name=textbridge --budget-amount=1USD \
    --threshold-rule=percent=0.01 --threshold-rule=percent=1.0 --quiet >/dev/null \
    && echo "Done: you'll get an email if anything ever costs money." \
    || echo "Couldn't set it automatically. Set it in the console: Billing > Budgets & alerts."
fi

say "Opening web access (ports 80 and 443)"
for p in 80 443; do
  rule="textbridge-allow-$p"
  gcloud compute firewall-rules describe "$rule" >/dev/null 2>&1 || \
    gcloud compute firewall-rules create "$rule" --network=default --allow="tcp:$p" \
      --target-tags=textbridge --source-ranges=0.0.0.0/0 --quiet >/dev/null
done

say "Making your passphrase"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/tb.tgz" "https://github.com/${REPO}/releases/latest/download/textbridge-linux-amd64.tar.gz"
tar -xzf "$tmp/tb.tgz" -C "$tmp" textbridge-gateway
PASSPHRASE=""
if ! gcloud compute instances describe "$NAME" --zone "$ZONE" >/dev/null 2>&1; then
  PASSPHRASE="$(TB_STATE_DIR="$tmp/st" "$tmp/textbridge-gateway" new-password)"
  HASH="$(cat "$tmp/st/password")"

  cat > "$tmp/startup.sh" <<EOF
#!/bin/bash
# Install textbridge on first boot only. Update later with: sudo textbridge update
if [ ! -x /opt/textbridge/bin/openmessage ]; then
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/deploy/setup.sh | bash
fi
EOF
  printf '%s' "$HASH" > "$tmp/hash"
  meta="startup-script=$tmp/startup.sh,tb-password-hash=$tmp/hash"

  say "Creating the free server (e2-micro, Oregon, 30 GB standard disk)"
  gcloud compute instances create "$NAME" --zone "$ZONE" \
    --machine-type=e2-micro \
    --image-family=debian-12 --image-project=debian-cloud \
    --boot-disk-size=30GB --boot-disk-type=pd-standard \
    --tags=textbridge \
    --metadata-from-file="$meta" \
    --quiet >/dev/null
else
  echo "Server '$NAME' already exists; keeping your current passphrase."
fi

IP="$(gcloud compute instances describe "$NAME" --zone "$ZONE" --format='value(networkInterfaces[0].accessConfigs[0].natIP)')"
URL="https://${IP//./-}.sslip.io"

say "Installing on the server (about 3-5 minutes)"
ok=""
for _ in $(seq 1 60); do
  if curl -fsS --max-time 5 "$URL/healthz" >/dev/null 2>&1; then ok=1; break; fi
  printf '.'
  sleep 10
done
echo
if [ -z "$ok" ]; then
  echo "Still installing or something went wrong. To see what happened:"
  echo "  gcloud compute instances get-serial-port-output $NAME --zone $ZONE | grep -i startup | tail -40"
  echo "Then run this command again."
fi

printf '\n\033[1;32m%s\033[0m\n\n' "Server ready."
echo "  Connector URL (paste into Claude):"
printf '\n      \033[1m%s/mcp\033[0m\n\n' "$URL"
if [ -n "$PASSPHRASE" ]; then
  echo "  Your passphrase. Save it in your password manager NOW; it won't be shown again:"
  printf '\n      \033[1m%s\033[0m\n\n' "$PASSPHRASE"
fi
echo "  Next, link your phone. Open an SSH window to the server:"
echo "      gcloud compute ssh $NAME --zone $ZONE"
echo "  and run:  sudo textbridge pair"
echo
