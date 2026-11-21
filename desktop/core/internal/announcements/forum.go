package announcements

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var errForumScanLimit = errors.New("其余论坛目录或条目等待后续检查")

func sourceForumPage(u *url.URL) bool {
	return u != nil && (u.Host == "kleiforums.com" || u.Host == "forums.kleientertainment.com")
}
func sourceForumTopic(u *url.URL) bool {
	return sourceForumPage(u) && u.RawQuery == "" && sourceTopic.MatchString(u.Path) &&
		u.EscapedPath() == (&url.URL{Path: u.Path}).EscapedPath()
}

func sourceIndexAuthor(main *html.Node) forumAuthor {
	meta := sourceOne(main, func(n *html.Node) bool { return sourceClass(n, "ipsDataItem_meta") })
	by := sourceOne(meta, func(n *html.Node) bool { return n.Parent == meta && n.Data == "span" })
	name, ok := strings.CutPrefix(sourceText(by), "By ")
	if !ok {
		return forumAuthor{}
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, ","))
	return forumAuthor{name, forumLinkedProfile(by, name)}
}

// A retained community post can exclude its author from the staff-only discovery
// rule only while its topic URL, title, opening time and listed author still match.
func sourceForumPost(data []byte, row sourceArticle, listed forumAuthor, retained bool) (*html.Node, forumPlayer, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, forumPlayer{}, err
	}
	mismatch := errors.New("论坛首帖的身份或正文未能核实")
	canonical := sourceOne(doc, func(n *html.Node) bool { return n.Data == "link" && sourceAttr(n, "rel") == "canonical" })
	title := sourceOne(doc, func(n *html.Node) bool { return n.Data == "h1" && sourceClass(n, "ipsType_pageTitle") })
	if (canonical != nil && sourceAttr(canonical, "href") != row.url) || (title != nil && sourceText(title) != row.title) {
		return nil, forumPlayer{}, mismatch
	}
	post := sourceOne(doc, func(n *html.Node) bool {
		return n.Data == "article" && (sourceClass(n, "cPost") || sourceClass(n, "ipsComment"))
	})
	meta := sourceOne(post, func(n *html.Node) bool { return sourceClass(n, "ipsComment_meta") })
	stamp := sourceOne(meta, func(n *html.Node) bool { return n.Data == "time" && sourceAttr(n, "datetime") != "" })
	day, e := time.Parse(time.RFC3339, sourceAttr(stamp, "datetime"))
	if e != nil || !day.Equal(row.day) {
		return nil, forumPlayer{}, mismatch
	}
	author := sourceOne(post, func(n *html.Node) bool { return sourceClass(n, "cAuthorPane") || sourceClass(n, "ipsComment_author") })
	name := sourceOne(author, func(n *html.Node) bool { return sourceClass(n, "cAuthorPane_author") })
	if listed.name != "" && sourceText(name) != listed.name {
		return nil, forumPlayer{}, mismatch
	}
	group := sourceOne(author, func(n *html.Node) bool { return sourceAttr(n, "data-role") == "group" })
	if group == nil {
		group = sourceOne(author, func(n *html.Node) bool {
			return n.Data == "li" && sourceClass(n, "ipsType_break") || sourceClass(n, "ipsType_light") || sourceClass(n, "ipsType_normal")
		})
	}
	body := sourceOne(post, func(n *html.Node) bool {
		if sourceAttr(n, "data-role") == "commentContent" {
			return true
		}
		if sourceClass(n, "ipsType_richText") {
			for p := n.Parent; p != nil && p != post; p = p.Parent {
				if sourceClass(p, "ipsComment_content") {
					return true
				}
			}
		}
		return false
	})
	if body == nil {
		return nil, forumPlayer{}, mismatch
	}
	strict := canonical != nil && title != nil && listed.name != "" && sourceAttr(group, "data-role") == "group"
	if retained && !strict {
		return nil, forumPlayer{}, mismatch
	}
	role := strings.ToLower(sourceText(group))
	if forumPlayerGroup(role) && (role != "new user" || sourceAttr(group, "data-role") == "group") {
		id := ""
		if strict {
			id = forumPostProfile(doc, name, row, listed.name)
			if listed.id != "" && id != "" && listed.id != id {
				return nil, forumPlayer{}, mismatch
			}
		}
		if strict {
			return nil, forumPlayer{role, id}, nil
		}
		return nil, forumPlayer{}, nil
	}
	switch role {
	case "administrators":
		if !retained && sourceAttr(group, "data-role") == "group" {
			badge := sourceOne(author, func(n *html.Node) bool {
				return n.Data == "img" && n.Parent != nil && n.Parent.Data == "li" &&
					sourceAttr(n.Parent, "data-role") == "group-icon" && sourceClass(n, "cAuthorGroupIcon") &&
					sourceAttr(n, "src") == sourceForum+"/uploads/team_kleistaff.png"
			})
			if badge != nil {
				return body, forumPlayer{}, nil
			}
		}
	case "developer", "klei developer", "klei entertainment", "klei staff":
		if !retained {
			return body, forumPlayer{}, nil
		}
	}
	return nil, forumPlayer{}, mismatch
}

// Klei's forum directory publishes the latest activity for each public board.
// A date preceding the entire window rules out new public threads on that board.
func (r sourceReader) forumInactive(base string) bool {
	const directory = sourceForum + "/forums/"
	data, err := r.exact(directory)
	if err != nil {
		return false
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return false
	}
	canonical := sourceOne(doc, func(n *html.Node) bool { return n.Data == "link" && sourceAttr(n, "rel") == "canonical" })
	if sourceAttr(canonical, "href") != directory {
		return false
	}
	for _, row := range sourceNodes(doc, func(n *html.Node) bool { return n.Data == "li" && sourceClass(n, "cForumRow") }) {
		title := sourceOne(row, func(n *html.Node) bool { return sourceClass(n, "ipsDataItem_title") })
		link := sourceOne(title, func(n *html.Node) bool { return n.Data == "a" && sourceAttr(n, "href") == base })
		id := sourceAttr(row, "data-forumid")
		if link == nil || id == "" || !strings.HasPrefix(base, directory+"forum/"+id+"-") {
			continue
		}
		last := sourceOne(row, func(n *html.Node) bool { return sourceClass(n, "ipsDataItem_lastPoster") })
		stamps := sourceNodes(last, func(n *html.Node) bool { return n.Data == "time" })
		if len(stamps) != 1 || stamps[0].Parent.Data != "a" {
			return false
		}
		u := sourceURL(sourceAttr(stamps[0].Parent, "href"))
		if !sourceForumTopic(u) {
			return false
		}
		stamp, err := time.Parse(time.RFC3339, sourceAttr(stamps[0], "datetime"))
		return err == nil && !stamp.IsZero() && stamp.Before(r.now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29))
	}
	return false
}

type forumListing struct {
	URL, Title, Author, ProfileID string
	Published                     time.Time
	Pinned                        bool
}
type forumIndexPage struct {
	Rows []forumListing
	More bool
}

func (r sourceReader) forumIndex(data []byte, base string) (forumIndexPage, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return forumIndexPage{}, err
	}
	list := sourceNodes(doc, func(n *html.Node) bool { return n.Data == "li" && sourceClass(n, "ipsDataItem") })
	if len(list) == 0 {
		return forumIndexPage{}, errors.New("论坛索引缺少条目")
	}
	next := sourceOne(doc, func(n *html.Node) bool {
		return sourceClass(n, "ipsPagination_next") && !sourceClass(n, "ipsPagination_inactive")
	})
	page := forumIndexPage{More: next != nil}
	var malformed error
	for _, n := range list {
		main := sourceOne(n, func(n *html.Node) bool { return sourceClass(n, "ipsDataItem_main") })
		title := sourceOne(main, func(n *html.Node) bool { return sourceClass(n, "ipsDataItem_title") })
		a := sourceOne(title, func(n *html.Node) bool {
			return n.Data == "a" && strings.Contains(sourceAttr(n, "href"), "/forums/topic/")
		})
		stamp := sourceOne(main, func(n *html.Node) bool { return n.Data == "time" && sourceAttr(n, "datetime") != "" })
		day, e := time.Parse(time.RFC3339, sourceAttr(stamp, "datetime"))
		if e != nil {
			malformed = errors.New("论坛条目缺少首发时间")
			continue
		}
		pin := sourceOne(n, func(n *html.Node) bool {
			return sourceClass(n, "fa-thumb-tack") || sourceClass(n, "fa-thumbtack") || strings.Contains(sourceAttr(n, "title"), "Pinned")
		}) != nil
		if !r.recent(day) {
			if !day.Before(r.now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)) {
				malformed = errors.New("论坛条目的首发日期晚于检查日期")
			}
			if !pin && day.Before(r.now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29)) {
				page.More = false
			}
			continue
		}
		u, e := url.Parse(sourceAbsolute(base, sourceAttr(a, "href")))
		if e == nil {
			u.RawQuery = ""
			u.Fragment = ""
		}
		if e != nil || !sourceForumTopic(sourceURL(u.String())) || sourceText(a) == "" {
			malformed = errors.New("论坛条目缺少可核实的首帖地址或标题")
			continue
		}
		author := sourceIndexAuthor(main)
		page.Rows = append(page.Rows, forumListing{u.String(), sourceText(a), author.name, author.id, day, pin})
	}
	return page, malformed
}

// A newly inserted topic can move the last item of the first page onto the
// second. Keep the original head until its non-pinned, date-sorted rows no
// longer overlap the current head, then restart the directory walk.
func (head forumIndexPage) overlaps(earlier forumIndexPage) bool {
	// A suffix of the new head must still be the unchanged prefix of the old
	// head. A deletion, reordering or match on a pinned topic cannot bridge it.
	current := make([]forumListing, 0, len(head.Rows))
	previous := make([]forumListing, 0, len(earlier.Rows))
	for _, row := range head.Rows {
		if !row.Pinned {
			current = append(current, row)
		}
	}
	for _, row := range earlier.Rows {
		if !row.Pinned {
			previous = append(previous, row)
		}
	}
	if len(previous) == 0 {
		return false
	}
	for i, row := range current {
		if row.URL != previous[0].URL {
			continue
		}
		suffix := current[i:]
		if len(suffix) > len(previous) {
			return false
		}
		for k, item := range suffix {
			other := previous[k]
			if item.URL != other.URL || item.Title != other.Title || item.Author != other.Author || item.ProfileID != other.ProfileID || !item.Published.Equal(other.Published) {
				return false
			}
		}
		return true
	}
	return false
}

func (r sourceReader) forum(offers sourceOffers, base string, release bool) error {
	if release {
		return r.releases(offers, base)
	}
	progress := r.forumProgress(base)
	defer func() { r.saveForumProgress(base, progress) }()
	var rows []sourceArticle
	var listings []forumListing
	seen := map[string]int{}
	authors := map[string]forumAuthor{}
	var failures []error
	addRows := func(page forumIndexPage) {
		for _, topic := range page.Rows {
			if topic.Title == "" || !r.recent(topic.Published) || !sourceForumTopic(sourceURL(topic.URL)) {
				continue
			}
			if i, ok := seen[topic.URL]; ok {
				// RSS can omit author metadata present on the matching board row.
				row := &listings[i]
				if row.Author == "" && topic.Author != "" && row.Title == topic.Title && row.Published.Equal(topic.Published) {
					row.Author, row.ProfileID = topic.Author, topic.ProfileID
					authors[topic.URL] = forumAuthor{topic.Author, topic.ProfileID}
				}
				continue
			}
			seen[topic.URL] = len(listings)
			listings = append(listings, topic)
			authors[topic.URL] = forumAuthor{topic.Author, topic.ProfileID}
			rows = append(rows, sourceArticle{topic.URL, topic.Title, topic.Published})
		}
	}
	canSave := true
	requests := 0
	for number := 1; ; number++ {
		if number > 1 && number <= len(progress.Pages) {
			page := progress.Pages[number-1]
			addRows(page)
			if !page.More {
				break
			}
			continue
		}
		raw := base
		if number > 1 {
			raw += fmt.Sprintf("page/%d/", number)
		}
		raw += "?sortby=start_date&sortdirection=desc"
		allow := func(u *url.URL) bool {
			x := sourceURL(raw)
			return sourceForumPage(u) && u.Path == x.Path && u.RawQuery == x.RawQuery
		}
		data, _ := r.cached(raw, allow, 30*time.Minute)
		// An unreachable head is already partial. Keep collecting older public
		// rows without spending the remaining request on a page we have on disk.
		if data == nil && !canSave && number > 1 {
			data, _ = r.cached(raw, allow, 24*time.Hour)
		}
		if data == nil {
			if requests >= 2 {
				failures = append(failures, errForumScanLimit)
				break
			}
			requests++
			var err error
			data, _, err = r.read(raw, allow)
			if err != nil {
				if number == 1 {
					if r.forumInactive(base) {
						progress.Topics = nil
						return nil
					}
					addRows(forumIndexPage{Rows: r.forumFeedTopics(base)})
				}
				failures = append(failures, err)
				canSave = false
				data, _ = r.cached(raw, allow, 24*time.Hour)
				if data == nil {
					break
				}
			}
		}
		page, err := r.forumIndex(data, base)
		if err != nil {
			if number == 1 && canSave {
				addRows(forumIndexPage{Rows: r.forumFeedTopics(base)})
			}
			failures = append(failures, err)
			canSave = false
		}
		addRows(page)
		if !page.More {
			if number == 1 && canSave {
				progress.Pages = nil
			} else if canSave {
				progress.Pages = append(progress.Pages, page)
			}
			break
		}
		if number == 1 {
			if len(progress.Pages) > 0 && page.overlaps(progress.Pages[0]) {
				addRows(progress.Pages[0])
				continue
			}
			progress.Pages = nil
			progress.Started = r.now
		}
		if canSave {
			progress.Pages = append(progress.Pages, page)
		}
	}
	// A failed directory request can outlast the HTML cache. Keep attempting
	// known opening posts within the window without treating them as a full index.
	if len(failures) != 0 {
		addRows(forumIndexPage{Rows: progress.Topics})
		for _, page := range progress.Pages {
			addRows(page)
		}
	}
	progress.Topics = listings
	// Fresh staff posts and verified community authors consume no network budget.
	var pending []sourceArticle
	players := make(map[string]bool)
	crossCheck := make(map[string]string)
	receipts := r.forumPlayerChecks()
	for _, row := range rows {
		author := authors[row.url]
		allow := func(u *url.URL) bool { return u != nil && u.String() == row.url }
		data, _ := r.cached(row.url, allow, 30*time.Minute)
		retained := data == nil
		if retained && author.name != "" {
			data, _ = r.cached(row.url, allow, 24*time.Hour)
		}
		if data != nil {
			delete(progress.Players, row.url)
			if body, player, err := sourceForumPost(data, row, author, retained); err == nil {
				if body != nil {
					r.readHTMLOffers(offers, body, false)
				} else if player.Group != "" {
					if player.ID != "" {
						players[player.ID] = true
					}
					progress.recordPlayer(row, author, player, r.forumDocumentTime(row.url))
				}
				continue
			}
		} else {
			if receipts[row.url].matches(row, author) || progress.Players[row.url].matches(row, author) {
				continue
			}
			crossCheck[row.url] = author.id
		}
		pending = append(pending, row)
	}
	// Only link authors across topics when the board itself supplies a profile ID.
	unverified := pending[:0]
	for _, row := range pending {
		if !players[crossCheck[row.url]] {
			unverified = append(unverified, row)
		}
	}
	pending = unverified
	sort.SliceStable(pending, func(i, j int) bool {
		a, b := progress.Attempts[pending[i].url], progress.Attempts[pending[j].url]
		if a.Equal(b) {
			return r.hints[pending[i].url] && !r.hints[pending[j].url]
		}
		return a.Before(b)
	})
	selected := pending[:min(5, len(pending))]
	attempted := make(map[string]bool)
	for _, row := range selected {
		if players[crossCheck[row.url]] {
			continue
		}
		attempted[row.url] = true
		progress.Attempts[row.url] = r.now
		u := sourceURL(row.url)
		data, _, e := r.read(row.url, func(dest *url.URL) bool {
			return sourceForumPage(dest) && dest.EscapedPath() == u.EscapedPath() && dest.RawQuery == ""
		})
		if e != nil {
			failures = append(failures, e)
			continue
		}
		delete(progress.Players, row.url)
		body, player, err := sourceForumPost(data, row, authors[row.url], false)
		if err != nil {
			failures = append(failures, err)
		} else if body != nil {
			r.readHTMLOffers(offers, body, false)
		} else if player.Group != "" {
			if player.ID != "" {
				players[player.ID] = true
			}
			progress.recordPlayer(row, authors[row.url], player, r.now)
		}
	}
	for _, row := range pending {
		if !attempted[row.url] && !players[crossCheck[row.url]] {
			failures = append(failures, errForumScanLimit)
			break
		}
	}
	return errors.Join(failures...)
}
