# portfolio-contact-api

A small Go API that receives the contact-form messages from my portfolio
(https://portfolio-aalia98.netlify.app). Standard library only, no framework.

## What it does

- `POST /api/contact` takes `{name, email, message}` as JSON.
- Validates every field (length limits, real email address, no header injection).
- Rate limit: 5 messages per IP per hour (HTTP 429 after that).
- Honeypot field `website`: bots that fill it get a fake success and nothing is stored.
- CORS: only the origins in `ALLOWED_ORIGINS` can call it from a browser.
- Saves each message to a JSON Lines file and, if SMTP is set, emails me a notification.
- `GET /healthz` for hosting health checks.

## Run locally

```bash
go test ./...
ALLOWED_ORIGINS=http://localhost:5173 go run .
curl -X POST localhost:8080/api/contact \
  -H 'Content-Type: application/json' \
  -d '{"name":"Sam","email":"sam@example.com","message":"Hello, I have a project."}'
```

## Environment variables

| Name | Meaning | Default |
| --- | --- | --- |
| `PORT` | Port to listen on | `8080` |
| `ALLOWED_ORIGINS` | Comma-separated browser origins allowed to call the API | `http://localhost:5173` |
| `DATA_FILE` | Where messages are appended | `messages.jsonl` |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS` | Optional email notification (for example Gmail with an app password) | off |
| `NOTIFY_TO` | Address that receives notifications | off |

## Deploy

A `Dockerfile` is included. On a free host (for example Render) create a Web Service from this
repo, set the variables above and use `/healthz` as the health check. Note: free hosts can have an
ephemeral disk, so the email notification is the reliable copy of each message.
