# bsky-webhook

[![status: experimental](https://img.shields.io/badge/status-experimental-blue)](https://tailscale.com/kb/1167/release-stages/#experimental)

Sends Slack webhook alerts for Bluesky messages using [Jetstream](https://github.com/bluesky-social/jetstream).

## Running

```bash
export BSKY_APP_PASSWORD=asdf-asdf-asdf
export SLACK_WEBHOOK_URL=https://tailscale.slack.com/...
go run ./cmd/bsky-webhook/ -bskyHandle me.example.com -watchWord "pangolin"
```

## Configuration

These configuration options are available as command-line flags and
environment variables. Those without defaults are required.

Here's the complete table based on the provided Go code:

| Command-line flag  | Environment variable | Default value                           | Description                                                                          |
| ------------------ | -------------------- | --------------------------------------- | ------------------------------------------------------------------------------------ |
| `-addr`            | `JETSTREAM_ADDRESS`  | Rotation of all public jetsream servers | The [jetstream](https://github.com/bluesky-social/jetstream) hostname to connect to. |
| `-bsky-handle`      | `BSKY_HANDLE`        | none                                    | The Bluesky handle of the account that will make API requests.                       |
| `-bsky-app-password` | `BSKY_APP_PASSWORD`  | none                                    | The Bluesky app password for authentication.                                         |
| `-slack-webhook-url` | `SLACK_WEBHOOK_URL`  | none                                    | The Slack webhook URL for sending notifications.                                     |
| `-bsky-server-url`   | `BSKY_SERVER_URL`    | "https://bsky.network"                  | The Bluesky PDS server to send API requests to URL.                                  |
| `-watch-word`       | `WATCH_WORD`         | "tailscale"                             | The word to watch out for; may support multiple words in the future.                 |

