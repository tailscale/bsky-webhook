package main

import (
	"fmt"
	"net/url"
)

type BskyMessage struct {
	Did    string      `json:"did"`
	Commit *BskyCommit `json:"commit"`
	Kind   string      `json:"kind"`
	Time   int64       `json:"time_us"`
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
	Text            string      `json:"text"`
	Embed           BskyEmbed   `json:"embed"`
	CreatedAtString string      `json:"createdAt"`
	Facets          []BskyFacet `json:"facets"`
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

type BskyFacet struct {
	Features []BskyFacetFeatures `json:"features"`
	Index    BskyFacetIndex      `json:"index"`
}

type BskyFacetFeatures struct {
	Type string `json:"$type"`
	Uri  string `json:"uri"`
	Did  string `json:"did"`
	Tag  string `json:"tag"`
}

type BskyFacetIndex struct {
	ByteEnd   int `json:"byteEnd"`
	ByteStart int `json:"byteStart"`
}

type BskyTextFragment struct {
	Text     string
	Features []BskyFacetFeatures
}

type SlackAttachment struct {
	AuthorName string `json:"author_name"`
	AuthorIcon string `json:"author_icon"`
	AuthorLink string `json:"author_link"`
	Text       string `json:"text"`
	ImageUrl   string `json:"image_url"`
	Footer     string `json:"footer"`
	Ts         string `json:"ts"`
}

type SlackBody struct {
	Text        string            `json:"text"`
	UnfurlLinks bool              `json:"unfurl_links"`
	UnfurlMedia bool              `json:"unfurl_media"`
	Attachments []SlackAttachment `json:"attachments"`
}
