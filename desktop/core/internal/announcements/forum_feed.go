package announcements

import (
	"encoding/xml"
	"net/mail"
	"strings"
)

// Both boards advertise a "Latest Topics" RSS alternate in their HTML head.
// The feed may contain player posts and can be shorter than the search window.
// Use only its topic references, then check authors and rewards in the first post.
func (r sourceReader) forumFeedTopics(board string) []forumListing {
	switch board {
	case sourceForum + "/forums/forum/72-dont-starve-together-developer-log/",
		sourceForum + "/forums/forum/66-dont-starve-together-general-discussion/":
	default:
		return nil
	}
	raw := strings.TrimSuffix(board, "/") + ".xml/"
	data, err := r.exact(raw)
	if err != nil {
		return nil
	}
	var rss struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Link  string          `xml:"link"`
			Items []sourceRSSItem `xml:"item"`
		} `xml:"channel"`
	}
	if xml.Unmarshal(data, &rss) != nil || (rss.Channel.Link != board && rss.Channel.Link != raw) {
		return nil
	}
	var rows []forumListing
	for _, item := range rss.Channel.Items {
		day, err := mail.ParseDate(item.Date)
		title := strings.TrimSpace(item.Title)
		if err == nil && r.recent(day) && sourceForumTopic(sourceURL(item.Link)) && title != "" {
			rows = append(rows, forumListing{URL: item.Link, Title: title, Published: day})
		}
	}
	return rows
}
