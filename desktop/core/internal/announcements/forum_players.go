package announcements

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type forumAuthor struct {
	name, id string
}

var forumProfilePath = regexp.MustCompile(`^/profile/([1-9][0-9]*)-[^/]+/?$`)

func forumProfileID(raw string) string {
	u := sourceURL(raw)
	if !sourceForumPage(u) || u.RawQuery != "" {
		return ""
	}
	if m := forumProfilePath.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	return ""
}

func forumLinkedProfile(by *html.Node, name string) string {
	link := sourceOne(by, func(n *html.Node) bool {
		return n.Data == "a" && sourceText(n) == name && forumProfileID(sourceAttr(n, "href")) != ""
	})
	return forumProfileID(sourceAttr(link, "href"))
}

func forumPlayerGroup(role string) bool {
	return role == "registered users" || role == "members" || role == "new user"
}

// Some Klei skins render the opening author's name as plain text, and publish
// the profile link in the topic's structured data instead.
func forumPostProfile(doc, by *html.Node, row sourceArticle, name string) string {
	if id := forumLinkedProfile(by, name); id != "" {
		return id
	}
	headers := sourceNodes(doc, func(n *html.Node) bool {
		return n.Data == "head" || n.Data == "header" && n.Parent != nil && n.Parent.Data == "body"
	})
	for _, header := range headers {
		for n := header.FirstChild; n != nil; n = n.NextSibling {
			if n.Data != "script" || sourceAttr(n, "type") != "application/ld+json" || n.FirstChild == nil {
				continue
			}
			var topic struct {
				Type                         string `json:"@type"`
				URL, Headline, DatePublished string
				PageStart                    int
				Author                       struct {
					Type      string `json:"@type"`
					Name, URL string
				}
			}
			if json.Unmarshal([]byte(n.FirstChild.Data), &topic) != nil || topic.Type != "DiscussionForumPosting" || topic.PageStart != 1 ||
				topic.URL != row.url || topic.Headline != row.title || topic.Author.Type != "Person" || topic.Author.Name != name {
				continue
			}
			stamp, err := time.Parse(time.RFC3339, topic.DatePublished)
			if err != nil {
				stamp, err = time.Parse("2006-01-02T15:04:05-0700", topic.DatePublished)
			}
			if err == nil && stamp.Equal(row.day) {
				return forumProfileID(topic.Author.URL)
			}
		}
	}
	return ""
}

// A manually checked, publicly indexed copy of the same opening post can carry
// its group evidence for this opening throughout the 30-day search window.
type forumPlayerCheck struct {
	URL, Title, Author, Group, Source, ProfileID string
	Published, Checked                           time.Time
}

func (r sourceReader) forumPlayerChecks() map[string]forumPlayerCheck {
	if r.dir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(r.dir, "forum-player-checks.json"))
	if err != nil {
		return nil
	}
	defer f.Close()
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	var checks []forumPlayerCheck
	if err != nil || len(data) > limit || json.Unmarshal(data, &checks) != nil {
		return nil
	}
	result := make(map[string]forumPlayerCheck)
	for _, c := range checks {
		u, src := sourceURL(c.URL), sourceURL(c.Source)
		age := r.now.Sub(c.Checked)
		if !r.recent(c.Published) || age < 0 || age >= 30*24*time.Hour || c.Published.After(c.Checked) || c.Author == "" || c.Title == "" || !forumPlayerGroup(strings.ToLower(c.Group)) ||
			!sourceForumTopic(u) ||
			!sourceForumPage(src) || src.Host != u.Host || src.EscapedPath() != u.EscapedPath() {
			continue
		}
		if src.RawQuery != "" {
			q := src.Query()
			if len(q) != 2 || len(q["comment"]) != 1 || len(q["do"]) != 1 ||
				!sourceGID.MatchString(q.Get("comment")) || q.Get("do") != "findComment" {
				continue
			}
		}
		result[c.URL] = c
	}
	return result
}

type forumPlayer struct {
	Group, ID string
}

func (c forumPlayerCheck) matches(row sourceArticle, author forumAuthor) bool {
	return c.URL == row.url && c.Author != "" && c.Author == author.name && c.Title == row.title && c.Published.Equal(row.day) &&
		(author.id == "" || c.ProfileID == "" || author.id == c.ProfileID)
}

type forumScanProgress struct {
	Started  time.Time
	Pages    []forumIndexPage
	Topics   []forumListing
	Attempts map[string]time.Time
	Players  map[string]forumPlayerCheck
}

func (r sourceReader) forumProgressPath(base string) string {
	return filepath.Join(r.dir, fmt.Sprintf("forum-progress-%x.json", sha256.Sum256([]byte(base))))
}

func (r sourceReader) forumProgress(base string) forumScanProgress {
	var s forumScanProgress
	if r.dir != "" {
		if f, err := os.Open(r.forumProgressPath(base)); err == nil {
			const limit = 4 << 20
			data, e := io.ReadAll(io.LimitReader(f, limit+1))
			f.Close()
			if e == nil && len(data) <= limit && json.Unmarshal(data, &s) != nil {
				s = forumScanProgress{}
			}
		}
	}
	if age := r.now.Sub(s.Started); age < 0 || age >= 24*time.Hour {
		// Older versions saved only the pagination cursor. Its rows can still
		// identify openings after the cursor itself is too old to resume.
		if len(s.Topics) == 0 && age >= 24*time.Hour && age < 30*24*time.Hour {
			for _, page := range s.Pages {
				s.Topics = append(s.Topics, page.Rows...)
			}
		}
		s.Pages = nil
	}
	if s.Attempts == nil {
		s.Attempts = map[string]time.Time{}
	}
	if s.Players == nil {
		s.Players = map[string]forumPlayerCheck{}
	}
	for raw, when := range s.Attempts {
		if age := r.now.Sub(when); age < 0 || age >= 30*24*time.Hour {
			delete(s.Attempts, raw)
		}
	}
	for raw, c := range s.Players {
		age := r.now.Sub(c.Checked)
		if age < 0 || age >= 30*24*time.Hour || !r.recent(c.Published) || c.Published.After(c.Checked) ||
			c.URL != raw || c.Source != raw || !sourceForumTopic(sourceURL(raw)) || !forumPlayerGroup(c.Group) {
			delete(s.Players, raw)
		}
	}
	return s
}

func (s *forumScanProgress) recordPlayer(row sourceArticle, author forumAuthor, player forumPlayer, checked time.Time) {
	s.Players[row.url] = forumPlayerCheck{URL: row.url, Title: row.title, Author: author.name, Published: row.day, Checked: checked, Group: player.Group, ProfileID: player.ID, Source: row.url}
}

func (r sourceReader) saveForumProgress(base string, s forumScanProgress) {
	if r.dir == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil || len(data) > 4<<20 {
		return
	}
	f, err := os.CreateTemp(r.dir, "source-forum-progress-")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), r.forumProgressPath(base))
	}
}

func (r sourceReader) forumDocumentTime(raw string) time.Time {
	info, err := os.Stat(filepath.Join(r.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))))
	if err != nil || info.ModTime().After(r.now) {
		return r.now
	}
	return info.ModTime()
}
