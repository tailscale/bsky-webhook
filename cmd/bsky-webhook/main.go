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
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/karalabe/go-bluesky"
)

type BskyMessage struct {
	Did    string      `json:"did"`
	Commit *BskyCommit `json:"commit"`
	Kind   string      `json:"kind"`
}

func (m *BskyMessage) toURL(handle *string) string {
	author := handle
	if author == nil {
		author = &m.Did
	}

	return fmt.Sprintf("https://bsky.app/profile/%s/post/%s", url.PathEscape(*author), url.PathEscape(m.Commit.Rkey))
}

type BskyCommit struct {
	Rev       string      `json:"rev"`
	Rkey      string      `json:"rkey"`
	Record    *BskyRecord `json:"record"`
	Operation string      `json:"operation"`
}

type BskyRecord struct {
	Text  string    `json:"text"`
	Embed BskyEmbed `json:"embed"`
}

type BskyEmbed struct {
	Images []BskyImage `json:"images"`
}

type BskyImage struct {
	Image BskyInnerImage `json:"image"`
}

type BskyInnerImage struct {
	Ref BskyImageRef `json:"ref"`
}

type BskyImageRef struct {
	Link string `json:"$link"`
}

type SlackAttachment struct {
	AuthorName string `json:"author_name"`
	AuthorIcon string `json:"author_icon"`
	AuthorLink string `json:"author_link"`
	Text       string `json:"text"`
	ImageUrl   string `json:"image_url"`
	Footer     string `json:"footer"`
}

type SlackBody struct {
	Text        string            `json:"text"`
	UnfurlLinks bool              `json:"unfurl_links"`
	UnfurlMedia bool              `json:"unfurl_media"`
	Attachments []SlackAttachment `json:"attachments"`
}

func envOr(key, defaultVal string) string {
	if result, ok := os.LookupEnv(key); ok {
		return result
	}
	return defaultVal
}

var addr = flag.String("addr", envOr("JETSTREAM_ADDRESS", ""), "jetstream websocket address")
var bskyHandle = flag.String("bskyHandle", envOr("BSKY_HANDLE", ""), "bluesky handle for auth")
var bskyAppKey = flag.String("bskyAppPassword", envOr("BSKY_APP_PASSWORD", ""), "bluesky app password for auth")
var webhookUrl = flag.String("slackWebhookUrl", envOr("SLACK_WEBHOOK_URL", ""), "slack webhook url")
var bskyServerUrl = flag.String("bskyServerUrl", envOr("BSKY_SERVER_URL", "https://bsky.network"), "bluesky pds server url")
var watchWord = flag.String("watchWord", envOr("WATCH_WORD", "tailscale"), "the word to watch out for. may be multiple words in futureee")

// https://github.com/bluesky-social/jetstream?tab=readme-ov-file#public-instances
var jetstreams = []string{
	"jetstream1.us-east.bsky.network",
	"jetstream2.us-east.bsky.network",
	"jetstream1.us-west.bsky.network",
	"jetstream2.us-west.bsky.network",
}

func main() {
	flag.Parse()

	if *webhookUrl == "" {
		log.Fatalf("missing slack webhook url in env")
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	go func() {
		<-interrupt
		os.Exit(0)
	}()

	currentAddr := *addr
	jetstreamIndex := 0 // start with first jetstream index

	for {
		if *addr == "" {
			currentAddr = jetstreams[jetstreamIndex]
		}

		wsUrl := url.URL{Scheme: "wss", Host: currentAddr, Path: "/subscribe", RawQuery: "wantedCollections=app.bsky.feed.post"}
		slog.Info("ws connecting", "url", wsUrl.String())

		err := websocketConnection(wsUrl)
		slog.Error("ws connection", "url", wsUrl, "err", err)

		if *addr == "" {
			// cycle between jetstreams if no override
			jetstreamIndex++
			if jetstreamIndex >= len(jetstreams) {
				jetstreamIndex = 0
			}
		}

		// TODO(erisa): exponential backoff
		time.Sleep(2 * time.Second)
	}
}

func websocketConnection(wsUrl url.URL) error {
	c, _, err := websocket.DefaultDialer.Dial(wsUrl.String(), nil)
	if err != nil {
		return fmt.Errorf("dial jetstream: %v", err)
	}
	defer c.Close()

	c.SetCloseHandler(func(code int, text string) error {
		return nil
	})

	ctx := context.Background()

	bsky, err := bluesky.Dial(ctx, *bskyServerUrl)
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

func readJetstreamMessage(jetstreamMessage []byte, bsky *bluesky.Client) error {
	var bskyMessage BskyMessage
	err := json.Unmarshal(jetstreamMessage, &bskyMessage)
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
			Text:       fmt.Sprintf("%s\n[View post on Bluesky ↗](%s)", bskyMessage.Commit.Record.Text, bskyMessage.toURL(&profile.Handle)),
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
	res, err := http.Post(*webhookUrl, "application/json", bytes.NewBuffer(body))
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
