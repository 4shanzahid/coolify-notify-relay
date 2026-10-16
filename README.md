# coolify-notify-relay

A small, dependency-free Go service that sends [Coolify](https://coolify.io) notifications to
**Google Chat**. It can optionally record deployments as **GitHub Deployments**.

Coolify can't post to Google Chat on its own, and it has one webhook per team, so every project's
events land at the same URL. This relay sits behind that webhook. It filters events by rules you
write, then posts readable cards to the right Chat spaces.

```
Coolify ──webhook──▶ relay ──▶ filter by event / project / environment / app ──▶ Google Chat
                                                                             └─▶ GitHub Deployments (optional)
```

## Features

- **Routing:** match events by event name, project, environment and app name (globs allowed). Send
  each route to its own Chat space.
- **Chat cards:** ✅ or ❌ status, commit SHA and message, branch, and buttons to open the app and the
  Coolify deployment logs. Each app's deploys are threaded together.
- **Reliable delivery:**
  - Replies to Coolify immediately.
  - Retries with exponential backoff, honouring `Retry-After`.
  - Removes duplicate events.
  - Respects Google Chat's limit of 1 message per second per space.
- **GitHub Deployments (optional):** creates a deployment for the exact commit and marks it success
  or failure. Supports GitHub App or token auth.
- **Small and safe:**
  - Standard library only, about a 19 MB distroless image running as non-root.
  - Secret webhook path.
  - Webhook tokens are never logged.
  - `/healthz` and Prometheus `/metrics` endpoints.

## Quick start

```bash
cp .env.example .env        # set RELAY_SECRET and GCHAT_DEPLOYMENTS
docker build -t coolify-notify-relay .
docker run --rm -p 8080:8080 --env-file .env coolify-notify-relay

# send Coolify's test payload
curl -X POST localhost:8080/hooks/coolify/$RELAY_SECRET --data @testdata/test.json
```

Then, in Coolify, go to **Notifications → Webhook**. Set the URL to
`http(s)://<relay-host>/hooks/coolify/<RELAY_SECRET>`, enable it, and choose the events you want.

## Configuration

Routing lives in [`config.json`](config.json). The default path is `/config/config.json`, and you can
change it with `CONFIG_PATH`. Secrets are passed as environment variables, which the config refers to by name.

```json
{
  "coolify": { "base_url_env": "COOLIFY_API_URL", "token_env": "COOLIFY_API_TOKEN" },
  "github": { "enabled": false },
  "routes": [
    {
      "name": "shop-dev-deploys",
      "match": {
        "events": ["deployment_success", "deployment_failed"],
        "projects": ["shop"],
        "environments": ["dev"],
        "applications": ["shop-*"]
      },
      "googlechat": { "webhook_env": "GCHAT_DEPLOYMENTS", "thread": "per_app" }
    }
  ]
}
```

- A `match` field you leave out matches anything. All fields must match. One event can match
  several routes.
- `thread` can be `per_app` (the default), `per_day` or `none`.
- Coolify's **test notification** goes to every configured Chat webhook.
- The relay won't start if the config is invalid: unknown fields, missing env vars or bad globs.

| Env var | Description |
|---|---|
| `RELAY_SECRET` | **Required.** At least 32 characters, used in the webhook path (`openssl rand -hex 24`) |
| `GCHAT_*` | Google Chat incoming webhook URLs, named in `config.json` |
| `COOLIFY_API_URL`, `COOLIFY_API_TOKEN` | Optional. A read-only Coolify API token, used to add commit details. Required for GitHub |
| `GITHUB_APP_ID` + `GITHUB_APP_PRIVATE_KEY`(`_FILE`), or `GITHUB_TOKEN` | GitHub Deployments only |
| `TZ`, `LOG_LEVEL`, `PORT`, `WORKERS`, `QUEUE_SIZE` | Optional |

## Deploying on Coolify

Deploy it as a **Dockerfile** application on the same server as Coolify:

1. **Network:** set a custom network alias (for example `notify-relay`) and give it no public domain.
   Then add `notify-relay` under **Settings → Advanced → allowed internal webhook hosts**. Coolify
   blocks private webhook targets unless they are listed there.
2. **Webhook URL:** `http://notify-relay:8080/hooks/coolify/<RELAY_SECRET>`. Use
   `COOLIFY_API_URL=http://coolify:8080` to reach the Coolify API.
3. **Health check:** keep Coolify's own health check **disabled**. The image has no shell or curl,
   so it uses its own Dockerfile `HEALTHCHECK` instead.
4. **Your routes:** mount your own `config.json` at `/config/config.json` as a Coolify file mount,
   or fork the repo and edit it there.

## GitHub Deployments

1. Create a GitHub App with **Deployments: read & write**, **Contents: read** and **Metadata: read**,
   then install it on your repos.
2. Set `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY` (or `GITHUB_TOKEN` with a fine-grained PAT that
   has the same permissions), plus the Coolify API vars.
3. Set `"github": {"enabled": true}` both at the top level and on the route.

Coolify doesn't send a "deployment started" event, so GitHub shows the final success or failure,
not "in progress".

## Development

Requires Go 1.27+ ([mise](https://mise.jdx.dev): `mise install`).

```bash
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

The payloads in `testdata/` match what Coolify v4.4.x sends.

## License

[MIT](LICENSE)
