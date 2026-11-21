package announcements

import (
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata"

	"golang.org/x/net/html"
)

func sourceMailPage(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Host == "mailchi.mp" {
		return u.RawQuery == "" && sourceMailSlug.MatchString(u.EscapedPath())
	}
	q := u.Query()
	return (u.Host == "us2.campaign-archive.com" || u.Host == "kleientertainment.us2.campaign-archive.com") && u.Path == "/" && len(q) == 2 && len(q["u"]) == 1 && q.Get("u") == sourceCampaign && len(q["id"]) == 1 && sourceMailID.MatchString(q.Get("id"))
}
func sourceMailHop(u *url.URL) bool {
	return sourceMailPage(u) || (u != nil && u.Host == "eepurl.com" && u.RawQuery == "" && sourceShortMail.MatchString(u.EscapedPath()))
}
func sourceMailBody(doc *html.Node) *html.Node {
	return sourceOne(doc, func(n *html.Node) bool {
		id := sourceAttr(n, "id")
		return id == "bodyTable" || id == "templateContainer"
	})
}

// Mailchimp's archive uses the publisher's calendar day. A unique dated subject
// and the edition's own permalink identify its full RSS copy when a short link is blocked.
func sourceMailEditions(rows []sourceArticle, items []sourceRSSItem) map[string]*html.Node {
	type edition struct{ day, title string }
	listed := map[edition]sourceArticle{}
	urlCount := map[string]int{}
	for _, row := range rows {
		key := edition{row.day.Format("2006-01-02"), row.title}
		urlCount[row.url]++
		if _, duplicate := listed[key]; duplicate {
			listed[key] = sourceArticle{}
		} else {
			listed[key] = row
		}
	}
	location, _ := time.LoadLocation("America/Vancouver") // embedded by time/tzdata
	matched := map[string]*html.Node{}
	seen := map[edition]bool{}
	for _, item := range items {
		when, err := mail.ParseDate(item.Date)
		if err != nil {
			continue
		}
		key := edition{when.In(location).Format("2006-01-02"), item.Title}
		row := listed[key]
		if row.url == "" || urlCount[row.url] != 1 {
			continue
		}
		if seen[key] {
			delete(matched, row.url)
			continue
		}
		seen[key] = true
		if !sourceMailPage(sourceURL(item.Link)) || (sourceMailPage(sourceURL(row.url)) && row.url != item.Link) {
			continue
		}
		doc, err := html.Parse(strings.NewReader(item.body()))
		if err != nil {
			continue
		}
		title := sourceOne(doc, func(n *html.Node) bool { return n.Data == "title" })
		body := sourceMailBody(doc)
		permalink := sourceOne(body, func(n *html.Node) bool {
			href := sourceAttr(n, "href")
			return n.Data == "a" && (href == item.Link || href == item.Link+"?e=[UNIQID]")
		})
		if title != nil && sourceText(title) == item.Title && permalink != nil {
			matched[row.url] = body
		}
	}
	return matched
}

func (r sourceReader) archive(offers sourceOffers, newsletter func() ([]sourceRSSItem, error)) error {
	data, err := r.exact(sourceMailArchive)
	if err != nil {
		return err
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	index := sourceOne(doc, func(n *html.Node) bool { return sourceAttr(n, "id") == "archive-list" })
	nodes := sourceNodes(index, func(n *html.Node) bool { return n.Data == "li" && sourceClass(n, "campaign") })
	if len(nodes) == 0 {
		return errors.New("未找到邮件列表")
	}
	var rows []sourceArticle
	for _, n := range nodes {
		m := sourceEditionDate.FindStringSubmatch(sourceText(n))
		if m == nil {
			continue
		}
		day, err := time.Parse("01/02/2006", m[1])
		if err != nil || !r.recent(day) {
			continue
		}
		a := sourceOne(n, func(n *html.Node) bool { return n.Data == "a" })
		raw := sourceAttr(a, "href")
		raw = strings.TrimPrefix(raw, "http://")
		if !strings.HasPrefix(raw, "https://") {
			raw = "https://" + raw
		}
		if sourceMailHop(sourceURL(raw)) && sourceText(a) != "" {
			rows = append(rows, sourceArticle{raw, sourceText(a), day})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	items, _ := newsletter() // If the feed is unavailable, read the archive's own links.
	editions := sourceMailEditions(rows, items)
	var failures []error
	for _, row := range r.articles(rows, 4) {
		if body := editions[row.url]; body != nil {
			r.readHTMLOffers(offers, body, true)
			continue
		}
		data, final, err := r.read(row.url, sourceMailHop)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !sourceMailPage(sourceURL(final)) {
			failures = append(failures, errors.New("邮件短链未抵达 Klei 的发行页"))
			continue
		}
		doc, e := html.Parse(strings.NewReader(string(data)))
		if e != nil {
			failures = append(failures, e)
			continue
		}
		title := sourceOne(doc, func(n *html.Node) bool { return n.Data == "title" })
		body := sourceMailBody(doc)
		canonical := sourceOne(doc, func(n *html.Node) bool { return n.Data == "input" && sourceAttr(n, "id") == "clipboardSource" })
		if canonical != nil && !sourceMailPage(sourceURL(sourceAttr(canonical, "value"))) {
			failures = append(failures, errors.New("邮件中的出版者地址不匹配"))
			continue
		}
		if title != nil && sourceText(title) != row.title || body == nil {
			failures = append(failures, errors.New("邮件存档正文或标题与目录不一致"))
			continue
		}
		r.readHTMLOffers(offers, body, true)
	}
	return errors.Join(failures...)
}

func (r sourceReader) collectMailArchiveLinks(item sourceRSSItem, archive sourceOffers) {
	date, err := mail.ParseDate(item.Date)
	if !sourceMailPage(sourceURL(item.Link)) || err != nil || date.After(r.now.Add(5*time.Minute)) {
		return
	}
	doc, err := html.Parse(strings.NewReader(item.body()))
	if err == nil {
		if body := sourceMailBody(doc); body != nil {
			for _, a := range sourceNodes(body, func(n *html.Node) bool { return n.Data == "a" }) {
				archive.add(rewardLinkCode(sourceAttr(a, "href")), false)
			}
		}
	}
}
