package announcements

import (
	stdhtml "html"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func rewardLinkCode(raw string) string {
	u := sourceURL(strings.TrimSpace(raw))
	if u != nil && u.Host == "steamcommunity.com" && strings.TrimRight(u.Path, "/") == "/linkfilter" {
		u = sourceURL(u.Query().Get("u"))
	}
	if u == nil || u.Host != "accounts.klei.com" || u.RawPath != "" || u.RawQuery != "" {
		return ""
	}
	m := sourceLink.FindStringSubmatch(u.Path)
	if m == nil {
		return ""
	}
	return m[1]
}
func sourceAttr(n *html.Node, k string) string {
	if n != nil {
		for _, a := range n.Attr {
			if a.Key == k {
				return a.Val
			}
		}
	}
	return ""
}
func sourceClass(n *html.Node, c string) bool {
	for _, v := range strings.Fields(sourceAttr(n, "class")) {
		if v == c {
			return true
		}
	}
	return false
}
func sourceNodes(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			if n.Data == "blockquote" || n.Data == "script" || n.Data == "style" {
				return
			}
			for _, a := range n.Attr {
				if a.Key == "data-ipsquote" {
					return
				}
			}
			if match(n) {
				found = append(found, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return found
}
func sourceOne(n *html.Node, match func(*html.Node) bool) *html.Node {
	items := sourceNodes(n, match)
	if len(items) > 0 {
		return items[0]
	}
	return nil
}
func sourceText(n *html.Node) string {
	var parts []string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil || n.Type == html.CommentNode || n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "blockquote") {
			return
		}
		if n.Type == html.TextNode {
			parts = append(parts, n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}
func sourcePoints(label string) bool {
	label = strings.Trim(strings.Join(strings.Fields(label), " "), " .!。！")
	return len([]rune(label)) <= 240 && (sourceEnglish.MatchString(label) || sourceChinese.MatchString(label))
}

type sourceOffers map[string]bool // true for confirmed points, false for official rewards of unconfirmed type
func (o sourceOffers) add(code string, points bool) {
	if code != "" {
		o[code] = o[code] || points
	}
}

// Klei sometimes links a reward to a single comma inside ordinary patch notes.
func hiddenPunctuation(label string) bool {
	label = strings.TrimSpace(label)
	return label != "" && utf8.RuneCountInString(label) <= 4 &&
		strings.IndexFunc(label, func(r rune) bool { return !unicode.IsPunct(r) }) < 0
}
func (r sourceReader) readHTMLOffers(o sourceOffers, doc *html.Node, newsletter bool) {
	for _, a := range sourceNodes(doc, func(n *html.Node) bool { return n.Data == "a" && sourceAttr(n, "href") != "" }) {
		code := rewardLinkCode(sourceAttr(a, "href"))
		if code == "" {
			continue
		}
		if r.recordRewardLink != nil {
			r.recordRewardLink(code)
		}
		img := sourceOne(a, func(n *html.Node) bool { return n.Data == "img" })
		label := sourceText(a)
		if img == nil && hiddenPunctuation(label) {
			o.add(code, false)
			continue
		}
		context := ""
		ambiguous := false
		for p := a.Parent; p != nil; p = p.Parent {
			if p.Data == "p" || p.Data == "li" {
				ambiguous = len(sourceNodes(p, func(n *html.Node) bool { return n.Data == "a" && sourceAttr(n, "href") != "" })) != 1
				context = sourceText(p)
				break
			}
		}
		if ambiguous {
			continue
		}
		if newsletter && label == "" && context == "" && sourceAttr(img, "alt") == "Sur-prize!" && sourceAttr(img, "src") == sourceGiftArt {
			o.add(code, false)
			continue
		}
		if context != "" {
			label = strings.TrimRight(strings.TrimSuffix(context, "https://accounts.klei.com/link/"+code), " ：:")
		}
		if label == "" {
			label = sourceAttr(img, "alt")
		}
		if sourcePoints(label) {
			o.add(code, true)
		}
	}
}
func bbRewardCode(match []string) string {
	for _, value := range match[1:3] {
		if value != "" {
			return rewardLinkCode(value)
		}
	}
	return rewardLinkCode(match[3])
}

func (o sourceOffers) fromBB(body string) {
	for _, line := range sourceBBParagraph.Split(stdhtml.UnescapeString(body), -1) {
		line = strings.TrimSpace(sourceBBStyle.ReplaceAllString(line, ""))
		matches := sourceBB.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			code := bbRewardCode(m)
			if code == "" {
				continue
			}
			if hiddenPunctuation(m[3]) {
				o.add(code, false)
				continue
			}
			if len(matches) == 1 {
				prose := strings.Replace(line, m[0], m[3], 1)
				prose = strings.TrimRight(strings.TrimSuffix(prose, "https://accounts.klei.com/link/"+code), " ：:")
				if sourcePoints(prose) {
					o.add(code, true)
				}
			}
		}
	}
}
