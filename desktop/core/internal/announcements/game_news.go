package announcements

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const sourceGameNews = "https://motd.klei.com/motd.json/?game=dst&platform=STEAM&locale=US&lang=english"

var gameShortPath = regexp.MustCompile(`^/[A-Za-z0-9_-]{1,160}/?$`)

type gameNotice struct {
	Title, Text, Href string
	Start             string `json:"start-time"`
	Finish            string `json:"finish-time"`
}

func (notice gameNotice) activeSince(now time.Time) (time.Time, bool) {
	start, err := strconv.ParseInt(notice.Start, 10, 64)
	if err != nil || start <= 0 || start > now.Unix() {
		return time.Time{}, false
	}
	if notice.Finish != "" {
		finish, err := strconv.ParseInt(notice.Finish, 10, 64)
		if err != nil || finish <= now.Unix() {
			return time.Time{}, false
		}
	}
	return time.Unix(start, 0), true
}

// Read the shortener's Location without visiting it. Some Klei short links lead
// directly to an account reward, while many others lead to unrelated sites.
func (r sourceReader) gameShortDestination(raw string) (string, error) {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodHead, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", sourceUserAgent)
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 301, 302, 303, 307, 308:
		target, err := resp.Location()
		if err != nil {
			return "", err
		}
		return target.String(), nil
	default:
		return "", fmt.Errorf("游戏公告短链接返回 HTTP %d", resp.StatusCode)
	}
}

func (r sourceReader) gameNews(offers sourceOffers) error {
	data, err := r.exact(sourceGameNews)
	if err != nil {
		return err
	}
	var board struct {
		Audience string
		Live     []struct {
			Category map[string][]gameNotice
		} `json:"boxes-live"`
	}
	if err := json.Unmarshal(data, &board); err != nil {
		return err
	}
	if board.Audience != "DST" || len(board.Live) == 0 || len(board.Live[0].Category) == 0 {
		return errors.New("游戏公告缺少 DST 栏目")
	}

	var failures []error
	shortLinks := map[string]string{}
	articles := map[string]bool{}
	for _, box := range board.Live {
		for _, category := range []string{"PatchNotes", "News", "Promo", "Skins", "Twitch"} {
			editorial := category == "News" || category == "PatchNotes"
			for _, notice := range box.Category[category] {
				if err := r.gameNoticeOffers(notice, editorial, offers, shortLinks, articles); err != nil {
					failures = append(failures, err)
					if errors.Is(err, errGameNewsLimit) {
						return errors.Join(failures...)
					}
				}
			}
		}
	}
	return errors.Join(failures...)
}

var errGameNewsLimit = errors.New("游戏公告链接超过上限")

func (r sourceReader) gameNoticeOffers(notice gameNotice, editorial bool, offers sourceOffers, shortLinks map[string]string, articles map[string]bool) error {
	start, active := notice.activeSince(r.now)
	u := sourceURL(notice.Href)
	if !active || u == nil {
		return nil
	}
	if editorial && u.Host == "klei.gg" && u.RawQuery == "" && gameShortPath.MatchString(u.EscapedPath()) {
		target, seen := shortLinks[notice.Href]
		if !seen {
			if len(shortLinks) >= 12 {
				return errGameNewsLimit
			}
			var err error
			target, err = r.gameShortDestination(notice.Href)
			shortLinks[notice.Href] = target
			if err != nil {
				return err
			}
		}
		u = sourceURL(target)
		if u == nil {
			return nil
		}
	}

	if code := rewardLinkCode(u.String()); u.Host == "accounts.klei.com" && code != "" {
		if r.recordRewardLink != nil {
			r.recordRewardLink(code)
		}
		if r.recent(start) {
			offers.add(code, sourcePoints(notice.Title+" "+notice.Text))
		}
		return nil
	}
	if !editorial || u.Host != "store.steampowered.com" || u.RawQuery != "" || !sourceSteamArticle.MatchString(u.EscapedPath()) {
		return nil
	}
	raw := strings.TrimSuffix(u.String(), "/")
	if articles[raw] {
		return nil
	}
	if len(articles) >= 12 {
		return errGameNewsLimit
	}
	articles[raw] = true
	return r.gameSteamArticle(raw, offers)
}

// The in-game label and start time describe the tile. Its rotating short link
// can point to a much older Steam event, which has its own publication date.
func (r sourceReader) gameSteamArticle(raw string, offers sourceOffers) error {
	data, err := r.exact(raw + "?l=english")
	if err != nil {
		return err
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var events []struct {
		GID       string
		ClanID    string `json:"clan_steamid"`
		AppID     int
		Published int
		Hidden    int
		News      struct {
			EventGID string `json:"event_gid"`
			PostTime int64
			Body     string
			Hidden   int
			Banned   int
		} `json:"announcement_body"`
	}
	config := sourceOne(doc, func(n *html.Node) bool { return sourceAttr(n, "id") == "application_config" })
	if err := json.Unmarshal([]byte(sourceAttr(config, "data-partnereventstore")), &events); err != nil {
		return err
	}
	gid := raw[strings.LastIndexByte(raw, '/')+1:]
	for _, event := range events {
		news := event.News
		if event.AppID != 322330 || event.ClanID != "103582791436356732" || event.GID != gid || news.EventGID != gid ||
			event.Published != 1 || event.Hidden != 0 || news.Hidden != 0 || news.Banned != 0 || news.Body == "" ||
			news.PostTime <= 0 || news.PostTime > r.now.Unix() {
			continue
		}
		if r.recent(time.Unix(news.PostTime, 0)) {
			offers.fromBB(news.Body)
		}
		if r.recordRewardLink != nil {
			for _, match := range sourceBB.FindAllStringSubmatch(stdhtml.UnescapeString(news.Body), -1) {
				if code := bbRewardCode(match); code != "" {
					r.recordRewardLink(code)
				}
			}
		}
		return nil
	}
	return errors.New("游戏公告链接的 Steam 文章身份或日期未能核实")
}
