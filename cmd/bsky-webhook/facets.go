package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

func (b BskyTextFragment) featureURI() string {
	for _, feat := range b.Features {
		switch feat.URI {
		case "app.bsky.richtext.facet#link":
			return feat.URI
		case "app.bsky.richtext.facet#mention":
			return fmt.Sprintf("https://bsky.app/profile/%s", feat.DID)
		case "app.bsky.richtext.facet#tag":
			return fmt.Sprintf("https://bsky.app/hashtag/%s", feat.Tag)
		}
	}
	return ""
}

func bskyMessageToSlackMarkup(msg BskyMessage) (string, error) {
	var sb strings.Builder

	fragments, err := facetsToFragments(msg)
	if err != nil {
		return "", err
	}

	for _, frag := range fragments {
		if uri := frag.featureURI(); uri != "" {
			fmt.Fprintf(&sb, "<%s|%s>", uri, frag.Text)
		} else {
			sb.WriteString(frag.Text)
		}
	}
	return sb.String(), nil
}

func facetsToFragments(bskyMessage BskyMessage) ([]BskyTextFragment, error) {
	facets := bskyMessage.Commit.Record.Facets
	textString := bskyMessage.Commit.Record.Text

	fragments := []BskyTextFragment{}

	// We use SortStable here as we want the original order of equal elements to stay the same.
	slices.SortStableFunc(facets, func(a, b BskyFacet) int {
		return cmp.Compare(a.Index.ByteStart, b.Index.ByteStart)
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
