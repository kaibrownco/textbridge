# textbridge

Lets Claude read and send your Google Messages texts (SMS **and RCS**)
from anywhere you use Claude, including the phone app and Duet, without
changing your default texting app.

**Setup guide: [docs/SETUP.md](docs/SETUP.md)**

## How it works

```
Claude ──https──▶ Caddy ──▶ textbridge-gateway ──▶ OpenMessage ──▶ your phone
 (custom          (TLS)      (passphrase login,      (Google Messages     (Google
  connector)                  OAuth for Claude)       link, as a tablet)   Messages)
```

- **[OpenMessage](https://github.com/MaxGhenis/openmessage)** does the
  Google Messages work: it links to your phone the way Messages for web
  does, syncs your full history, and exposes MCP tools (read, search, send).
  It links as a *tablet*, so your normal Messages for web keeps working.
  We build it unmodified at the commit pinned in [`UPSTREAM`](UPSTREAM).
- **textbridge-gateway** (this repo, Go standard library only) is the lock
  on the door. Claude connects through standard MCP OAuth: dynamic client
  registration, authorization code with PKCE, rotating refresh tokens. You
  approve each new connection with your passphrase. Only Claude's own
  redirect hosts can register. Five wrong passphrases lock sign-in for 15
  minutes. Tokens are stored hashed. OpenMessage itself only listens on
  localhost.
- **Caddy** provides the https address, using a free
  `<your-ip>.sslip.io` hostname and a Let's Encrypt certificate.

It runs on Google Cloud's always-free e2-micro server.

## Updating

Bump the commit in `UPSTREAM` (or change the gateway) and push to `main`.
GitHub Actions tests, builds, and publishes a release. Then on the server:
`sudo textbridge update`.

## Caveats

- OpenMessage uses Google Messages' unofficial web protocol. A Google change
  can break it until it is updated upstream.
- Your phone must be online.
- If Google expires the session, run `sudo textbridge pair` again.

## Development

```bash
go test ./...
```

License: public domain ([Unlicense](LICENSE)), same as OpenMessage.
