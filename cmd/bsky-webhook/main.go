// Program bsky-webhook receives posts from Bluesky and routes mentions of the
// designated keyword(s) to a Slack webhook.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bluesky-social/jetstream/pkg/models"
	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	bluesky "github.com/tailscale/go-bluesky"
	"github.com/tailscale/setec/client/setec"
	"tailscale.com/tsnet"
)

var (
	addr = flag.String("addr", envOr("JETSTREAM_ADDRESS", ""),
		"jetstream websocket address")
	bskyHandle = flag.String("bsky-handle", envOr("BSKY_HANDLE", ""),
		"bluesky handle for auth (required)")
	bskyAppKey = flag.String("bsky-app-password", envOr("BSKY_APP_PASSWORD", ""),
		"bluesky app password for auth (required)")
	webhookURL = flag.String("slack-webhook-url", envOr("SLACK_WEBHOOK_URL", ""),
		"slack webhook URL (required)")
	bskyServerURL = flag.String("bsky-server-url", envOr("BSKY_SERVER_URL",
		"https://bsky.social"), "bluesky PDS server URL")
	watchWord = flag.String("watch-word", envOr("WATCH_WORD", "tailscale"),
		"the word to watch out for. may be multiple words in future (required)")

	secretsURL = flag.String("secrets-url", envOr("SECRETS_URL", ""),
		"the URL of a secrets server (if empty, no server is used)")
	secretsPrefix = flag.String("secrets-prefix", envOr("SECRETS_PREFIX", ""),
		"the prefix to prepend to secret names fetched from --secrets-url")
	tsHostname = flag.String("ts-hostname", envOr("TS_HOSTNAME", ""),
		"the Tailscale hostname the server should advertise (if empty, runs locally)")
	tsStateDir = flag.String("ts-state-dir", envOr("TS_STATE_DIR", ""),
		"the Tailscale state directory path (optional)")
)

// Public addresses of jetstream websocket services.
// See: https://github.com/bluesky-social/jetstream?tab=readme-ov-file#public-instances
var jetstreams = []string{
	"jetstream1.us-east.bsky.network",
	"jetstream2.us-east.bsky.network",
	"jetstream1.us-west.bsky.network",
	"jetstream2.us-west.bsky.network",
}

// zstdDecoder is used as a stateless decoder for Jetstream messages.
// Only the DecodeAll method may be used.
var zstdDecoder *zstd.Decoder

// httpClient must be used for all HTTP requests. It is a variable so that it
// can be replaced with a proxy.
var httpClient = http.DefaultClient

func init() {
	// Jetstream uses a custom zstd dictionary, so make sure we do the same.
	var err error
	zstdDecoder, err = zstd.NewReader(nil, zstd.WithDecoderDicts(models.ZSTDDictionary))
	if err != nil {
		log.Panicf("failed to create zstd decoder: %v", err)
	}
}

func main() {
	flag.Parse()
	// TODO(creachadair): Usage text.

	switch {
	case *webhookURL == "" && *secretsURL == "":
		log.Fatal("missing slack webhook URL (SLACK_WEBHOOK_URL)")
	case *bskyServerURL == "":
		log.Fatal("missing Bluesky server URL (BSKY_SERVER_URL)")
	case *bskyHandle == "":
		log.Fatal("Missing Bluesky account handle (BSKY_HANDLE)")
	case *bskyAppKey == "" && *secretsURL == "":
		log.Fatal("missing Bluesky app secret (BSKY_APP_PASSWORD)")
	case *watchWord == "":
		log.Fatal("missing watchword")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *tsHostname != "" {
		ts := &tsnet.Server{
			Hostname: *tsHostname,
			Dir:      *tsStateDir,
		}
		if _, err := ts.Up(ctx); err != nil {
			log.Fatalf("starting tsnet for %q: %v", *tsHostname, err)
		}

		// Ensure HTTP and TCP connections go via Tailscale so ACLs work.
		httpClient = ts.HTTPClient()
		websocket.DefaultDialer.NetDialContext = ts.Dial
		log.Printf("running in tsnet as %q", *tsHostname)
	}

	if *secretsURL != "" {
		webhookSecret := path.Join(*secretsPrefix, "slack-webhook-url")
		appKeySecret := path.Join(*secretsPrefix, "bluesky-app-key")
		st, err := setec.NewStore(ctx, setec.StoreConfig{
			Client:  setec.Client{Server: *secretsURL, DoHTTP: httpClient.Do},
			Secrets: []string{webhookSecret, appKeySecret},
		})
		if err != nil {
			log.Fatalf("initialize secrets store: %v", err)
		}
		*webhookURL = st.Secret(webhookSecret).GetString()
		*bskyAppKey = st.Secret(appKeySecret).GetString()
		log.Printf("Fetched client secrets from %q", *secretsURL)
	}

	nextAddr := nextWSAddress()
	for ctx.Err() == nil {
		wsURL := url.URL{
			Scheme:   "wss",
			Host:     nextAddr(),
			Path:     "/subscribe",
			RawQuery: "wantedCollections=app.bsky.feed.post",
		}
		slog.Info("ws connecting", "url", wsURL.String())

		err := websocketConnection(ctx, wsURL)
		slog.Error("ws connection", "url", wsURL, "err", err)

		// TODO(erisa): exponential backoff
		time.Sleep(2 * time.Second)
	}
}

func envOr(key, defaultVal string) string {
	if result, ok := os.LookupEnv(key); ok {
		return result
	}
	return defaultVal
}

// nextWSAddress returns a function that, when called, reports the address to
// which websocket connections should be directed.
func nextWSAddress() func() string {
	if *addr != "" {
		return func() string { return *addr }
	}
	cur := 0
	return func() string {
		out := jetstreams[cur]
		cur++
		if cur >= len(jetstreams) {
			cur = 0
		}
		return out
	}
}

func websocketConnection(ctx context.Context, wsUrl url.URL) error {
	// add compression headers
	headers := http.Header{}
	headers.Add("Socket-Encoding", "zstd")

	c, _, err := websocket.DefaultDialer.Dial(wsUrl.String(), headers)
	if err != nil {
		return fmt.Errorf("dial jetstream: %v", err)
	}
	defer c.Close()

	c.SetCloseHandler(func(code int, text string) error {
		return nil
	})

	bsky, err := bluesky.DialWithClient(ctx, *bskyServerURL, httpClient)
	if err != nil {
		return fmt.Errorf("dial bsky: %w", err)
	}
	defer bsky.Close()

	err = bsky.Login(ctx, *bskyHandle, *bskyAppKey)
	if err != nil {
		return fmt.Errorf("login bsky: %w", err)
	}

	for ctx.Err() == nil {
		// bail if we take too long for a read
		c.SetReadDeadline(time.Now().Add(time.Second * 5))

		_, jetstreamMessage, err := c.ReadMessage()
		if err != nil {
			return err
		}

		err = readJetstreamMessage(ctx, jetstreamMessage, bsky)
		if err != nil {
			msg := jetstreamMessage[:min(32, len(jetstreamMessage))]
			log.Printf("error reading jetstream message %q: %v", msg, err)
			continue
		}
	}
	return ctx.Err()
}

func readJetstreamMessage(ctx context.Context, jetstreamMessageEncoded []byte, bsky *bluesky.Client) error {
	// Decompress the message
	m, err := zstdDecoder.DecodeAll(jetstreamMessageEncoded, nil)
	if err != nil {
		slog.Error("failed to decompress message", "error", err)
		return fmt.Errorf("failed to decompress message: %w", err)
	}
	jetstreamMessage := m

	var bskyMessage BskyMessage
	err = json.Unmarshal(jetstreamMessage, &bskyMessage)
	if err != nil {
		return err
	}

	// we can ignore these messages
	if bskyMessage.Kind != "commit" || bskyMessage.Commit == nil || bskyMessage.Commit.Operation != "create" || bskyMessage.Commit.Record == nil || bskyMessage.Commit.Rkey == "" {
		return nil
	}

	// parse timestamp user provided when posting
	postTime, err := time.Parse(time.RFC3339, bskyMessage.Commit.Record.CreatedAt)
	if err != nil {
		return err
	}

	// if too old, ignore
	if time.Since(postTime) > time.Hour*24 {
		return nil
	}

	if strings.Contains(strings.ToLower(bskyMessage.Commit.Record.Text), strings.ToLower(*watchWord)) {
		jetstreamMessageStr := string(jetstreamMessage)

		go func() {
			profile, err := getBskyProfile(ctx, bskyMessage, bsky)
			if err != nil {
				slog.Error("fetch profile", "err", err, "msg", jetstreamMessageStr)
				return
			}

			// ignore users that are muted by the bluesky account running the service
			if profile.Viewer.Muted {
				slog.Info("skipped post from muted user", "post", bskyMessage.toURL(&profile.Handle))
				return
			}

			var imageURL string

			if len(bskyMessage.Commit.Record.Embed.Images) != 0 {
				imageURL = fmt.Sprintf("https://cdn.bsky.app/img/feed_fullsize/plain/%s/%s", bskyMessage.DID, bskyMessage.Commit.Record.Embed.Images[0].Image.Ref.Link)
			}

			err = sendToSlack(ctx, jetstreamMessageStr, bskyMessage, imageURL, *profile, postTime)
			if err != nil {
				slog.Error("slack error", "err", err)
			}
		}()
	}

	return nil
}

func getBskyProfile(ctx context.Context, bskyMessage BskyMessage, bsky *bluesky.Client) (*bluesky.Profile, error) {
	profile, err := bsky.FetchProfile(ctx, bskyMessage.DID)
	if err != nil {
		return nil, err
	}

	// TODO(erisa): is there a better way to handle no avatar?
	if profile.AvatarURL == "" {
		profile.AvatarURL = "https://up.erisa.uk/blueskydefaultavatar.png"
	}

	// if the user has an invalid (unverified) handle, links with handles will break
	if profile.Handle == "handle.invalid" {
		profile.Handle = profile.DID
	}

	return profile, nil
}

func sendToSlack(ctx context.Context, jetstreamMessageStr string, bskyMessage BskyMessage, imageURL string, profile bluesky.Profile, postTime time.Time) error {
	var messageText string
	var err error

	messageText, err = bskyMessageToSlackMarkup(bskyMessage)
	if err != nil {
		return err
	}

	attachments := []SlackAttachment{
		{
			AuthorName: fmt.Sprintf("%s (@%s)", profile.Name, profile.Handle),
			AuthorIcon: profile.AvatarURL,
			AuthorLink: fmt.Sprintf("https://bsky.app/profile/%s", profile.Handle),
			Text:       fmt.Sprintf("%s\n<%s|View post on Bluesky ↗>", messageText, bskyMessage.toURL(&profile.Handle)),
			ImageUrl:   imageURL,
			Footer:     "Posted",
			Ts:         strconv.FormatInt(postTime.Unix(), 10),
		},
	}

	body, err := json.Marshal(SlackBody{
		Attachments: attachments,
		UnfurlLinks: true,
		UnfurlMedia: true,
	})

	if err != nil {
		log.Printf("failed to marshal text: %v", err)

	}
	req, err := http.NewRequestWithContext(ctx, "POST", *webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		slog.Error("failed to post to slack", "msg", jetstreamMessageStr)
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, err := io.ReadAll(res.Body)
		if err != nil {
			slog.Error("bad error code from slack and fail to read body", "statusCode", res.StatusCode, "msg", jetstreamMessageStr)
			return err
		}
		slog.Error("error code response from slack", "statusCode", res.StatusCode, "responseBody", string(body), "msg", jetstreamMessageStr)
		return fmt.Errorf("slack: %s %s", res.Status, string(body))
	}
	return nil
}
