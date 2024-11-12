// Program bsky-webhook receives webhooks from Bluesky and routes mentions of
// the designated keyword(s) to a Slack channel.
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
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/karalabe/go-bluesky"
	"github.com/klauspost/compress/zstd"
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
		"https://bsky.network"), "bluesky pds server URL (required)")
	watchWord = flag.String("watch-word", envOr("WATCH_WORD", "tailscale"),
		"the word to watch out for. may be multiple words in future (required)")
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

func init() {
	var err error
	zstdDecoder, err = zstd.NewReader(nil)
	if err != nil {
		log.Panicf("failed to create zstd decoder: %v", err)
	}
}

func main() {
	flag.Parse()
	// TODO(creachadair): Usage text.

	switch {
	case *webhookURL == "":
		log.Fatal("missing slack webhook URL (SLACK_WEBHOOK_URL)")
	case *bskyServerURL == "":
		log.Fatal("missing Bluesky server URL (BSKY_SERVER_URL)")
	case *bskyHandle == "":
		log.Fatal("Missing Bluesky account handle (BSKY_HANDLE)")
	case *bskyAppKey == "":
		log.Fatal("missing Bluesky app secret (BSKY_APP_PASSWORD)")
	case *watchWord == "":
		log.Fatal("missing watchword")
	}

	nextAddr := nextWSAddress()
	for {
		wsURL := url.URL{
			Scheme:   "wss",
			Host:     nextAddr(),
			Path:     "/subscribe",
			RawQuery: "wantedCollections=app.bsky.feed.post",
		}
		slog.Info("ws connecting", "url", wsURL.String())

		err := websocketConnection(wsURL)
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

func websocketConnection(wsUrl url.URL) error {
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

	ctx := context.Background()

	bsky, err := bluesky.Dial(ctx, *bskyServerURL)
	if err != nil {
		log.Fatal("dial bsky: ", err)
	}
	defer bsky.Close()

	err = bsky.Login(ctx, *bskyHandle, *bskyAppKey)
	if err != nil {
		log.Fatal("login bsky: ", err)
	}

	for {
		// bail if we take too long for a read
		c.SetReadDeadline(time.Now().Add(time.Second * 5))

		_, jetstreamMessage, err := c.ReadMessage()
		if err != nil {
			return err
		}

		err = readJetstreamMessage(jetstreamMessage, bsky)
		if err != nil {
			log.Println("error reading jetstream message: ", jetstreamMessage, err)
			continue
		}
	}
}

func readJetstreamMessage(jetstreamMessageEncoded []byte, bsky *bluesky.Client) error {
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

	if strings.Contains(bskyMessage.Commit.Record.Text, *watchWord) {
		jetstreamMessageStr := string(jetstreamMessage)

		go func() {
			profile, err := getBskyProfile(bskyMessage, bsky)
			if err != nil {
				slog.Error("fetch profile", "err", err, "msg", jetstreamMessageStr)
				return
			}

			var imageURL string

			if len(bskyMessage.Commit.Record.Embed.Images) != 0 {
				imageURL = fmt.Sprintf("https://cdn.bsky.app/img/feed_fullsize/plain/%s/%s", bskyMessage.Did, bskyMessage.Commit.Record.Embed.Images[0].Image.Ref.Link)
			}

			err = sendToSlack(jetstreamMessageStr, bskyMessage, imageURL, *profile)
			if err != nil {
				slog.Error("slack error", "err", err)
			}
		}()
	}

	return nil
}

func getBskyProfile(bskyMessage BskyMessage, bsky *bluesky.Client) (*bluesky.Profile, error) {
	profile, err := bsky.FetchProfile(context.Background(), bskyMessage.Did)
	if err != nil {
		return nil, err
	}

	// TODO(erisa): is there a better way to handle no avatar?
	if profile.AvatarURL == "" {
		profile.AvatarURL = "https://up.erisa.uk/blueskydefaultavatar.png"
	}

	return profile, nil
}

func sendToSlack(jetstreamMessageStr string, bskyMessage BskyMessage, imageURL string, profile bluesky.Profile) error {
	attachments := []SlackAttachment{
		{
			AuthorName: fmt.Sprintf("%s (@%s)", profile.Name, profile.Handle),
			AuthorIcon: profile.AvatarURL,
			AuthorLink: fmt.Sprintf("https://bsky.app/profile/%s", profile.Handle),
			Text:       fmt.Sprintf("%s\n<%s|View post on Bluesky ↗>", bskyMessage.Commit.Record.Text, bskyMessage.toURL(&profile.Handle)),
			ImageUrl:   imageURL,
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
	res, err := http.Post(*webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		slog.Error("failed to post to slack", "msg", jetstreamMessageStr)
		return err
	}

	if res.StatusCode != http.StatusOK {
		body, err := io.ReadAll(res.Body)
		if err != nil {
			slog.Error("bad error code from slack and fail to read body", "statusCode", res.StatusCode, "msg", jetstreamMessageStr)
			return err
		}
		defer res.Body.Close()

		slog.Error("error code response from slack", "statusCode", res.StatusCode, "responseBody", string(body), "msg", jetstreamMessageStr)
		return fmt.Errorf("slack: %s %s", res.Status, string(body))
	}
	return nil
}
