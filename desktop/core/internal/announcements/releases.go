package announcements

import (
	"bytes"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var errReleaseIdentity = errors.New("PC 更新目录、版本或正文未能核实")

func releasePage(raw string) func(*url.URL) bool {
	want := sourceURL(raw)
	return func(u *url.URL) bool {
		return want != nil && sourceForumPage(u) && u.EscapedPath() == want.EscapedPath() && u.RawQuery == want.RawQuery
	}
}

func releaseDocument(data []byte, raw string) (*html.Node, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	canonical := sourceOne(doc, func(n *html.Node) bool { return n.Data == "link" && sourceAttr(n, "rel") == "canonical" })
	from, want := sourceURL(sourceAttr(canonical, "href")), sourceURL(raw)
	if !sourceForumPage(from) || want == nil || from.EscapedPath() != want.EscapedPath() || from.RawQuery != "" && from.RawQuery != want.RawQuery {
		return nil, errReleaseIdentity
	}
	wrap := sourceOne(doc, func(n *html.Node) bool { return sourceAttr(n, "id") == "elCmsPageWrap" })
	if wrap == nil {
		return nil, errReleaseIdentity
	}
	return wrap, nil
}

func releaseArticle(n *html.Node, base string) (sourceArticle, error) {
	a := sourceOne(n, func(n *html.Node) bool { return n.Data == "a" && sourceClass(n, "cRelease") })
	u := sourceURL(sourceAbsolute(base, sourceAttr(a, "href")))
	m := sourceBuildDate.FindStringSubmatch(sourceText(a))
	if !sourceForumPage(u) || u.RawQuery != "" || !sourceRelease.MatchString(u.EscapedPath()) || len(m) == 0 {
		return sourceArticle{}, errReleaseIdentity
	}
	build := strings.Fields(m[0])[0]
	slug := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/game-updates/dst/"), "/")
	id, ok := strings.CutPrefix(slug, build+"-r")
	day, err := time.Parse("1/2/06", m[1])
	if !ok || id != sourceAttr(a, "data-releaseid") || err != nil {
		return sourceArticle{}, errReleaseIdentity
	}
	return sourceArticle{sourceForum + "/game-updates/dst/" + slug + "/", build, day}, nil
}

// Each row has a publisher-supplied release date. Only a verified descending
// directory can use the first older date as the end of the search window.
func (r sourceReader) releaseRows(base string, cutoff time.Time, wanted func(time.Time) bool, pageLimit int) ([]sourceArticle, error) {
	var rows []sourceArticle
	var failures []error
	seen := map[string]bool{}
	var previous time.Time
	raw := base
	for page := 1; page <= pageLimit; page++ {
		data, _, err := r.read(raw, releasePage(raw))
		if err != nil {
			failures = append(failures, err)
			data, _ = r.cached(raw, releasePage(raw), 24*time.Hour)
			if data == nil {
				break
			}
		}
		wrap, err := releaseDocument(data, raw)
		if err != nil {
			failures = append(failures, err)
			break
		}
		column := sourceOne(wrap, func(n *html.Node) bool { return sourceClass(n, "cReleaseColumn") })
		list := sourceOne(column, func(n *html.Node) bool { return sourceClass(n, "cCmsListing") })
		if list == nil {
			failures = append(failures, errReleaseIdentity)
			break
		}
		count, older := 0, false
		for n := list.FirstChild; n != nil; n = n.NextSibling {
			if n.Type != html.ElementNode || n.Data != "li" {
				continue
			}
			count++
			row, err := releaseArticle(n, base)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !previous.IsZero() && row.day.After(previous) {
				failures = append(failures, errReleaseIdentity)
			}
			previous = row.day
			if row.day.Before(cutoff) {
				older = true
			}
			if wanted(row.day) && !seen[row.url] {
				seen[row.url] = true
				rows = append(rows, row)
			}
		}
		if count == 0 {
			failures = append(failures, errReleaseIdentity)
			break
		}
		if older {
			break
		}
		next := sourceOne(column, func(n *html.Node) bool { return sourceClass(n, "ipsPagination_next") })
		if sourceClass(next, "ipsPagination_inactive") {
			break
		}
		a := sourceOne(next, func(n *html.Node) bool { return n.Data == "a" && sourceAttr(n, "rel") == "next" })
		u := sourceURL(sourceAbsolute(raw, sourceAttr(a, "href")))
		expected := base + "page/" + strconv.Itoa(page+1) + "/"
		if !sourceForumPage(u) || u.EscapedPath() != sourceURL(expected).EscapedPath() || u.RawQuery != "" && u.RawQuery != "d=7" {
			failures = append(failures, errReleaseIdentity)
			break
		}
		raw = u.String()
		if page == pageLimit {
			failures = append(failures, errForumScanLimit)
		}
	}
	return rows, errors.Join(failures...)
}

func (r sourceReader) releaseOffers(data []byte, row sourceArticle, offers sourceOffers, archived bool) error {
	wrap, err := releaseDocument(data, row.url)
	if err != nil {
		return err
	}
	title := sourceOne(wrap, func(n *html.Node) bool { return n.Data == "h1" && sourceClass(n, "ipsType_pageTitle") })
	if !strings.HasSuffix(sourceText(title), " - "+row.title) {
		return errReleaseIdentity
	}
	if archived {
		badge := sourceOne(title.Parent, func(n *html.Node) bool {
			return sourceClass(n, "ipsBadge") && n.Parent.Data == "p" && n.Parent.Parent == title.Parent &&
				strings.HasPrefix(sourceText(n), "Release Date: ")
		})
		day, err := time.Parse("1/2/06", strings.TrimPrefix(sourceText(badge), "Release Date: "))
		if err != nil || !day.Equal(row.day) {
			return errReleaseIdentity
		}
	}
	article := sourceOne(wrap, func(n *html.Node) bool { return n.Data == "article" && sourceClass(n, "ipsContained") })
	sections := sourceNodes(article, func(n *html.Node) bool { return n.Data == "section" && sourceClass(n, "ipsType_richText") })
	if len(sections) == 0 {
		return errReleaseIdentity
	}
	for _, section := range sections {
		if sourceText(section) == "" && sourceOne(section, func(n *html.Node) bool { return n.Data == "img" && sourceAttr(n, "src") != "" }) == nil {
			return errReleaseIdentity
		}
	}
	for _, section := range sections {
		if archived {
			// The active points list supplies the type in a separate step. The
			// official article can put its only reward link on an unlabelled image.
			for _, a := range sourceNodes(section, func(n *html.Node) bool { return n.Data == "a" }) {
				offers.add(rewardLinkCode(sourceAttr(a, "href")), false)
			}
		} else {
			r.readHTMLOffers(offers, section, false)
		}
	}
	return nil
}

func (r sourceReader) releases(offers sourceOffers, base string) error {
	cutoff := r.now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29)
	rows, err := r.releaseRows(base, cutoff, r.recent, 8)
	return errors.Join(err, r.readReleaseOffers(offers, rows, 12, false))
}

func (r sourceReader) archivedReleasePoints(offers sourceOffers, days []time.Time) error {
	wanted := make(map[time.Time]bool)
	var earliest time.Time
	today := r.now.UTC().Truncate(24 * time.Hour)
	for _, stamp := range days {
		day := stamp.UTC().Truncate(24 * time.Hour)
		if day.IsZero() || day.After(today) {
			continue
		}
		wanted[day] = true
		if earliest.IsZero() || day.Before(earliest) {
			earliest = day
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	rows, err := r.releaseRows(sourceReleases, earliest, func(day time.Time) bool { return wanted[day] }, 4)
	// This release can still be read when its listing ages beyond four pages.
	// A known article does not establish coverage of the rest of that day.
	june11 := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	if err != nil && wanted[june11] {
		seed := sourceArticle{sourceReleases + "736805-r2749/", "736805", june11}
		if !slices.ContainsFunc(rows, func(row sourceArticle) bool { return row.url == seed.url }) {
			rows = append(rows, seed)
		}
	}
	return errors.Join(err, r.readReleaseOffers(offers, rows, 6, true))
}

func (r sourceReader) readReleaseOffers(offers sourceOffers, rows []sourceArticle, limit int, archived bool) error {
	var failures []error
	var pending []sourceArticle
	for _, row := range rows {
		// Older RSS descriptions can omit every hyperlink. Keep the verified
		// article body for a day, including all of its additional sections.
		body, _ := r.cached(row.url, releasePage(row.url), 24*time.Hour)
		if body == nil || r.releaseOffers(body, row, offers, archived) != nil {
			pending = append(pending, row)
		}
	}
	selected := r.articles(pending, limit)
	if len(selected) < len(pending) {
		failures = append(failures, errForumScanLimit)
	}
	for _, row := range selected {
		body, _, err := r.read(row.url, releasePage(row.url))
		if err == nil {
			err = r.releaseOffers(body, row, offers, archived)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
