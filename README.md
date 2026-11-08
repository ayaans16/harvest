# harvest 🌳

A cute animated voxel tree that grows from your GitHub stats. Drop it in your profile README.

![harvest demo](docs/demo.gif)

| Tree part | Stat |
|---|---|
| Canopy size | contributions in the last year |
| Trunk height | account age |
| Red apples | stars (one each, up to 12) |
| Pink blossoms | followers |
| Ground flowers | public repos |
| Butterflies | pull requests |

```md
![my harvest tree](https://harvest.ayaanshaikh.ca/YOUR_USERNAME.gif)
```

Everything is Go and the standard library (plus `autocert` for HTTPS): a tiny software voxel renderer, a GitHub API client, and an HTTP server.

## Run it

```bash
go run ./cmd/harvest render -user demo          # offline preview -> demo.gif
GITHUB_TOKEN=... go run ./cmd/harvest render -user YOU
HARVEST_ADDR=:8080 go run ./cmd/harvest serve   # http://localhost:8080
```

`GITHUB_TOKEN` (any token, no scopes needed) is optional but unlocks the contributions and pull-request counts and lifts the API rate limit.

| Env var | Purpose |
|---|---|
| `HARVEST_DOMAIN` | Production: serve HTTPS for this domain with automatic Let's Encrypt certs |
| `GITHUB_TOKEN` | Contribution counts, higher rate limit |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | Enables "Sign in with GitHub" |
| `HARVEST_ADDR` | Dev listen address (default `:8080`) |

## GitHub Developer Program badge

The [GitHub Developer Program](https://github.com/developer-program) badge comes from building on the GitHub platform. harvest does that with a GitHub OAuth app:

1. Register at <https://github.com/settings/developers> → **New OAuth App**.
   Homepage `https://harvest.ayaanshaikh.ca`, callback `https://harvest.ayaanshaikh.ca/callback`.
2. Put the client id and secret into `/etc/harvest.env` on the server.
3. Join the Developer Program from your GitHub account and point it at this app.

## Deploy to an Ubuntu VPS (no Caddy, no nginx)

The Go binary binds ports 80/443 itself and gets certificates with `autocert`.

1. DNS: add an `A` record `harvest.ayaanshaikh.ca` → your VPS IP.
2. Open ports 80 and 443 (`sudo ufw allow 80,443/tcp`).
3. From this repo: `./deploy/deploy.sh you@your-vps`
4. Edit `/etc/harvest.env` on the VPS, then `sudo systemctl restart harvest`.
