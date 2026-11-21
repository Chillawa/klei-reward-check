package announcements

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const sourceRewardTypes = "https://dontstarve.wiki.gg/wiki/Klei_Rewards"

var wikiPointsAmount = regexp.MustCompile("^(?:[1-9][0-9]*|[1-9][0-9]{0,2}(?:,[0-9]{3})+)$")

type wikiRewardKind struct {
	pointsOnly bool
	active     bool
	start      time.Time
	photoMonth time.Time
}

type wikiRewardTable struct {
	byURL     map[string]wikiRewardKind
	hasActive bool
}

func (r sourceReader) corroboratePoints(offers sourceOffers) error {
	return r.matchWikiPoints(offers, false)
}

func (r sourceReader) listedActivePoints(candidates sourceOffers) error {
	return r.matchWikiPoints(candidates, true)
}

func (r sourceReader) matchWikiPoints(offers sourceOffers, activeOnly bool) error {
	read := r.rewardTypes
	if read == nil {
		read = r.readRewardTypes
	}
	var table wikiRewardTable
	for code, points := range offers {
		if points && !activeOnly {
			continue
		}
		if table.byURL == nil {
			var err error
			table, err = read()
			if err != nil {
				return err
			}
			if activeOnly && !table.hasActive {
				return errors.New("未识别现行或永久奖励表")
			}
		}
		entry := table.byURL["https://accounts.klei.com/link/"+code]
		offers[code] = entry.pointsOnly && (!activeOnly || entry.active)
	}
	return nil
}

func (r sourceReader) readRewardTypes() (wikiRewardTable, error) {
	var table wikiRewardTable
	body, err := r.exact(sourceRewardTypes)
	if err != nil {
		return table, err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return table, err
	}
	// The wiki corroborates a type or listing. Klei decides whether a link is still redeemable.
	table.byURL = make(map[string]wikiRewardKind)
	valid := false
	for _, node := range sourceNodes(doc, func(n *html.Node) bool { return n.Data == "table" && sourceClass(n, "wikitable") }) {
		section := rewardTableSection(node)
		if section == "" {
			continue
		}
		active := section != "Expired_Reward_Links"
		if readRewardRows(node, active, table.byURL) {
			valid = true
			table.hasActive = table.hasActive || active
		}
	}
	if !valid {
		return table, errors.New("未识别奖励种类表")
	}
	return table, nil
}

func rewardTableSection(table *html.Node) string {
	for n := table.PrevSibling; n != nil; n = n.PrevSibling {
		if n.Type != html.ElementNode {
			continue
		}
		heading := n
		if sourceClass(n, "mw-heading") {
			heading = sourceOne(n, func(n *html.Node) bool { return n.Data == "h2" || n.Data == "h3" })
		}
		if heading == nil || heading.Data != "h2" && heading.Data != "h3" {
			continue
		}
		if heading.Data != "h3" {
			return ""
		}
		section := sourceOne(heading, func(n *html.Node) bool {
			switch sourceAttr(n, "id") {
			case "Current_Reward_Links", "Permanent_Reward_Links", "Expired_Reward_Links":
				return true
			}
			return false
		})
		return sourceAttr(section, "id")
	}
	return ""
}

func readRewardRows(table *html.Node, active bool, kinds map[string]wikiRewardKind) bool {
	header := false
	hasStart := false
	sourceColumn := -1
	for group := table.FirstChild; group != nil; group = group.NextSibling {
		if group.Data != "thead" && group.Data != "tbody" {
			continue
		}
		for row := group.FirstChild; row != nil; row = row.NextSibling {
			if row.Data != "tr" {
				continue
			}
			var cells []*html.Node
			for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Data == "td" || cell.Data == "th" {
					cells = append(cells, cell)
				}
			}
			if len(cells) == 0 {
				continue
			}
			if !header {
				names := []string{"Link to Reward", "Klei Points", "Spools"}
				for i, name := range names {
					if len(cells) <= i || cells[i].Data != "th" || sourceText(cells[i]) != name {
						return false
					}
				}
				header = true
				hasStart = len(cells) > 3 && cells[3].Data == "th" && sourceText(cells[3]) == "Start Date"
				for i, cell := range cells {
					if cell.Data == "th" && sourceText(cell) == "Link Source" {
						sourceColumn = i
					}
				}
				continue
			}
			links := sourceNodes(cells[0], func(n *html.Node) bool { return n.Data == "a" })
			points := len(links) == 1 && len(cells) >= 3 && wikiPointsAmount.MatchString(sourceText(cells[1])) && sourceText(cells[2]) == "-"
			for _, cell := range cells[:min(3, len(cells))] {
				if cell.Data != "td" || sourceAttr(cell, "colspan") != "" || sourceAttr(cell, "rowspan") != "" {
					points = false
				}
			}
			for _, a := range links {
				href := sourceAttr(a, "href")
				if code := rewardLinkCode(href); code != "" && href == "https://accounts.klei.com/link/"+code {
					previous, seen := kinds[href]
					entry := previous
					entry.pointsOnly = points && (!seen || previous.pointsOnly)
					entry.active = active || previous.active
					if active && hasStart && len(cells) > 3 {
						source := ""
						if points && sourceColumn >= 0 && sourceColumn < len(cells) {
							source = sourceText(cells[sourceColumn])
						}
						entry.readPublicationDate(sourceText(cells[3]), source)
					}
					kinds[href] = entry
				}
			}
		}
	}
	return header
}

func (entry *wikiRewardKind) readPublicationDate(date, source string) {
	day, err := time.Parse("January 2, 2006", date)
	if err == nil {
		entry.start = day
	}
	source = strings.ToLower(source)
	if !strings.Contains(source, "qr") && !strings.Contains(source, "promo photo") && !strings.Contains(source, "promotional photo") {
		return
	}
	if day.IsZero() {
		day, _ = time.Parse("January 2006", date)
	}
	if !day.IsZero() {
		entry.photoMonth = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
}

// Dates guide the search through Klei's own PC archive. The wiki alone cannot supply a claim.
func (table wikiRewardTable) missingPublications(recent, archive sourceOffers, now time.Time) (int, []time.Time) {
	missing := 0
	dates := map[time.Time]bool{}
	for raw, entry := range table.byURL {
		if !entry.active || !entry.pointsOnly {
			continue
		}
		code := rewardLinkCode(raw)
		_, inRecent := recent[code]
		_, inArchive := archive[code]
		if inRecent || inArchive || code == "" {
			continue
		}
		missing++
		if !entry.start.IsZero() && !entry.start.After(now) {
			dates[entry.start] = true
		}
	}
	days := make([]time.Time, 0, len(dates))
	for day := range dates {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].After(days[j]) })
	return missing, days
}
