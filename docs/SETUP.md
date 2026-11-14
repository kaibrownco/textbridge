# Setting up textbridge (about 30 minutes, one time)

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
  shown only once.

## 4. Link your phone

In the same SSH window:

```bash
sudo textbridge pair
```

Follow the steps it prints. In short: open a **private/incognito** Chrome
window, sign in at messages.google.com/web, copy the `cookie` request
header from the developer tools' Network tab, paste it, press Enter and
then Ctrl+D, and tap the matching emoji on your phone.

Your normal Messages for web keeps working: textbridge links as a tablet,
which has its own slot.

Your full text history then syncs in the background. That can take a
while the first time.

## 5. Add it to Claude

1. In Claude (web or desktop): **Customize → Connectors → + → Add custom connector**.
2. Name: `Texts`. URL: your connector URL from step 3.
3. Click **Connect**. A page asks for your passphrase. Enter it.

It's now available everywhere you use Claude, including the phone app and Duet.

**Recommended:** in the connector's settings, set the send tools
(`send_message`, `send_to_conversation`, `send_group_message`,
`send_media_to_conversation`, `react_to_message`) to **ask before using**,
so nothing goes out to a client without your OK.

## If something stops working

In the server's SSH window:

| Problem | Command |
|---|---|
| Is it running? Is my phone linked? | `sudo textbridge status` |
| Texts stopped syncing | `sudo textbridge pair` |
| Get the latest version | `sudo textbridge update` |
| Lost your phone / passphrase leaked | `sudo textbridge new-password` |
| See what's happening | `sudo textbridge logs` |

Your phone must be on and online for texts to go through, the same as
with Messages for web.
