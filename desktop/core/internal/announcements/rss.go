package announcements

import (
	"encoding/xml"
	"errors"
	"net/mail"
	"strings"

	"golang.org/x/net/html"
)

type sourceRSSItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	Date    string `xml:"pubDate"`
	Body    string `xml:"description"`
	Content string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
}

func (r sourceReader) readRSSItems(raw string) ([]sourceRSSItem, error) {
	data, err := r.exact(raw)
	if err != nil {
		return nil, err
	}
	var feed struct {
		Items []sourceRSSItem `xml:"channel>item"`
	}
	if err = xml.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	if len(feed.Items) == 0 {
		return nil, errors.New("RSS 缺少文章")
	}
	return feed.Items, nil
}

func (item sourceRSSItem) body() string {
	if item.Content != "" {
		return item.Content
	}
	return item.Body
}

func (r sourceReader) collectRSSOffers(raw, kind string, items []sourceRSSItem, offers sourceOffers) {
	for _, item := range items {
		when, err := mail.ParseDate(item.Date)
		if err != nil || !r.recent(when) {
			continue
		}
		u := sourceURL(item.Link)
		if u == nil {
			continue
		}
		switch kind {
		case "mail":
			if !sourceMailPage(u) {
				continue
			}
		case "release":
			if !sourceForumPage(u) || !sourceRelease.MatchString(u.EscapedPath()) || u.RawQuery != "" {
				continue
			}
		case "steam":
			if u.RawQuery != "" && u.RawQuery != "l="+strings.TrimPrefix(raw, sourceSteamRSS) {
				continue
			}
			if !(u.Host == "store.steampowered.com" && sourceSteamArticle.MatchString(u.EscapedPath()) || u.Host == "steamcommunity.com" && sourceSteamCommunity.MatchString(u.EscapedPath())) {
				continue
			}
		}
		doc, err := html.Parse(strings.NewReader(item.body()))
		if err == nil {
			r.readHTMLOffers(offers, doc, kind == "mail")
		}
	}
}
