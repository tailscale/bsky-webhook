package main

import (
	"fmt"
	"slices"
	"strings"
)

func bskyMessageToSlackMarkup(bskyMessage BskyMessage) (string, error) {
	var slackStringBuilder strings.Builder

	fragments, err := facetsToFragments(bskyMessage)
	if err != nil {
		return "", err
	}

	for _, fragment := range fragments {
		if fragment.Features == nil {
			slackStringBuilder.WriteString(fragment.Text)
		} else {
			uri := ""
			for _, feature := range fragment.Features {
				if feature.Type == "app.bsky.richtext.facet#link" {
					uri = feature.Uri
					break
				} else if feature.Type == "app.bsky.richtext.facet#mention" {
					uri = fmt.Sprintf("https://bsky.app/profile/%s", feature.Did)
					break
				} else if feature.Type == "app.bsky.richtext.facet#tag" {
					uri = fmt.Sprintf("https://bsky.app/hashtag/%s", feature.Tag)
				}
			}
			if uri != "" {
				slackStringBuilder.WriteString(fmt.Sprintf("<%s|%s>", uri, fragment.Text))
			} else {
				slackStringBuilder.WriteString(fragment.Text)
			}
		}
	}

	return slackStringBuilder.String(), nil
}

func facetsToFragments(bskyMessage BskyMessage) ([]BskyTextFragment, error) {
	facets := bskyMessage.Commit.Record.Facets
	textString := bskyMessage.Commit.Record.Text

	fragments := []BskyTextFragment{}

	slices.SortStableFunc(facets, func(a, b BskyFacet) int {
		return a.Index.ByteStart - b.Index.ByteStart
	})

	textCursor := 0
	facetCursor := 0

	for facetCursor < len(facets) {
		currentFacet := facets[facetCursor]

		if textCursor < currentFacet.Index.ByteStart {
			fragments = append(fragments, BskyTextFragment{Text: textString[textCursor:currentFacet.Index.ByteStart]})
		} else if textCursor > currentFacet.Index.ByteStart {
			facetCursor++
			continue
		}

		if currentFacet.Index.ByteStart < currentFacet.Index.ByteEnd {
			fragmentText := textString[currentFacet.Index.ByteStart:currentFacet.Index.ByteEnd]

			// dont add the features if the text is blank
			if strings.TrimSpace(fragmentText) == "" {
				fragments = append(fragments, BskyTextFragment{
					Text: textString[currentFacet.Index.ByteStart:currentFacet.Index.ByteEnd],
				})
			} else {
				fragments = append(fragments, BskyTextFragment{
					Text:     textString[currentFacet.Index.ByteStart:currentFacet.Index.ByteEnd],
					Features: currentFacet.Features,
				})
			}
		}
		textCursor = currentFacet.Index.ByteEnd
		facetCursor++
	}
	if textCursor < len(textString) {
		fragments = append(fragments, BskyTextFragment{Text: textString[textCursor:]})
	}

	return fragments, nil
}
