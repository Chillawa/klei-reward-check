package announcements

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// AccountOffers extracts literal signup reward buttons from Klei's authenticated
// account rewards template. The parser returns false for a login or unrelated page.
func AccountOffers(body []byte) ([]string, bool) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	root := sourceOne(doc, func(n *html.Node) bool {
		return n.Data == "div" && sourceAttr(n, "id") == "kleiapp" && sourceClass(n, "account-rewards-page")
	})
	if root == nil {
		return nil, false
	}
	var codes []string
	for _, a := range sourceNodes(root, func(n *html.Node) bool {
		return n.Data == "a" && sourceClass(n, "btn") && n.Parent.Data == "p" &&
			strings.TrimSpace(sourceAttr(n.Parent, "v-if")) == "!signupClaimed"
	}) {
		target := strings.TrimPrefix(sourceAttr(a, "href"), "https://accounts.klei.com")
		if match := sourceLink.FindStringSubmatch(target); match != nil {
			codes = append(codes, match[1])
		}
	}
	return codes, true
}
