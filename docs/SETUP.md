# Setting up textbridge (about 30 minutes, one time)

## Fastest way: one command in Cloud Shell

1. Open <https://console.cloud.google.com>, make sure your account is
   activated (not just the free trial), and tap the **Cloud Shell** icon
   (`>_`) in the top bar.
2. Paste this and press Enter:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/kaibrownco/textbridge/main/deploy/gcp-create.sh | bash
   ```

   It creates the free server, opens web access, sets a $1 budget alert,
   installs everything, and prints your **connector URL** and **passphrase**.
3. Open the `/pair` link it prints to link your phone (step 4), then add the connector to Claude (step 5).

The manual steps below do the same thing by hand.

You'll need: your computer with Chrome, your Android phone, and a credit
card for Google Cloud's sign-up (the server itself is free).

## 1. Google Cloud account and a $1 alert

1. Go to <https://console.cloud.google.com> and sign in with your Google account.
2. Accept the terms and add a billing account (your card). New accounts also
   get trial credit; the server we make stays free after it runs out.
3. In the search bar, open **Budgets & alerts** → **Create budget**.
   Set the amount to **$1** and keep the email alerts on. If anything ever
   starts costing money, you'll get an email.

## 2. Create the free server

Search for **VM instances** (enable the Compute Engine API if asked), then
**Create instance**. Change only these settings:

| Setting | Value |
|---|---|
| Name | `textbridge` |
| Region | **us-west1 (Oregon)** (must be us-west1, us-central1 or us-east1 to be free) |
| Machine type | **E2 → e2-micro** |
| Boot disk → Change | **Debian 12**, disk type **Standard persistent disk**, size **30 GB** |
| Firewall | tick **Allow HTTP traffic** and **Allow HTTPS traffic** |

The boot disk type matters: the default "Balanced" disk is not free.

Click **Create**. The cost estimate on the right may show a few dollars;
the free tier is applied on the bill, not in that estimate.

## 3. Install textbridge

When the server shows a green check, click **SSH** next to it. A terminal
window opens. Paste this and press Enter:

```bash
curl -fsSL https://raw.githubusercontent.com/kaibrownco/textbridge/main/deploy/setup.sh | sudo bash
```

It takes a few minutes. At the end it prints:

- your **connector URL** (ends in `/mcp`)
- your **passphrase**. Save it in your password manager right away; it is
  shown only once. (If Claude is helping you in Chrome, it can read the
  passphrase straight from the Cloud Shell output and type it in for you
  in steps 4 and 5, so you never have to touch the terminal.)

## 4. Link your phone

Open **`<your connector URL without /mcp>/pair`** (for example
`https://35-252-173-21.sslip.io/pair`) and follow the steps on the page. In short: install the free
**Cookie-Editor** extension (cookie-editor.com) in **Safari or Firefox**
(not Chrome) and allow it in private windows; open a private window and
sign in at messages.google.com/web; click Cookie-Editor › Export ›
**Header String**; paste that into the page with your passphrase; tap the
emoji the page shows. (Copying the `Cookie` request header from the
developer tools' Network tab works too.)

**Don't get the cookie from Chrome.** (The /pair page itself can be open
in any browser, so Claude in Chrome can fill in the passphrase there.) Chrome binds Google sign-ins to itself
(device-bound session credentials), so cookies copied out of Chrome stop
working on the server within a couple of hours.

Your normal Messages for web keeps working: textbridge links as a tablet,
which has its own slot. Your text history then syncs in the background.

If texts ever stop coming through, use the same page to re-link.
(`sudo textbridge pair` on the server does the same thing from a terminal.)

## 5. Add it to Claude

1. In Claude (web or desktop): **Customize → Connectors → + → Add custom connector**.
2. Name: `Texts`. URL: your connector URL from step 3.
3. Click **Connect**. A page asks for your passphrase. Enter it.

It's now available everywhere you use Claude, including the phone app and Duet.

Claude is already told to show you every draft and wait for your yes
before sending. As a second lock, in the connector's settings set
`send_message`, `send_to_conversation` and `react_to_message` to
**ask before using**.

## If something stops working

In the server's SSH window:

| Problem | Command |
|---|---|
| Is it running? Is my phone linked? | `sudo textbridge status` |
| Texts stopped syncing | re-link at `…/pair` (or `sudo textbridge pair`) |
| Old texts missing | `sudo textbridge backfill` |
| Get the latest version | `sudo textbridge update` |
| Lost your phone / passphrase leaked | `sudo textbridge new-password` |
| See what's happening | `sudo textbridge logs` |

Your phone must be on and online for texts to go through, the same as
with Messages for web.
