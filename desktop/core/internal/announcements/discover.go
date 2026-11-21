package announcements

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Offer is a reward found in an official publication or on the account rewards page.
type Offer struct {
	Code   string `json:"code"`
	Points bool   `json:"points"`
}

func (r sourceReader) readOffers(feed string, gifts bool, accountOffers ...string) ([]Offer, error) {
	r.rewardTypes = sync.OnceValues(r.readRewardTypes)
	r.hints = r.feedHints(feed)
	// A button, image or arbitrary word can link a reward. Keep these URLs
	// for exact matches against the active points list, even when gifts are enabled.
	articleLinks := sourceOffers{}
	var articleMu sync.Mutex
	r.recordRewardLink = func(code string) {
		articleMu.Lock()
		articleLinks.add(code, false)
		articleMu.Unlock()
	}
	steamArchive, mailArchive, supportLinks := sourceOffers{}, sourceOffers{}, sourceOffers{}
	newsletter := sync.OnceValues(func() ([]sourceRSSItem, error) { return r.readRSSItems(sourceMailRSS) })
	rss := func(raw, kind string) func(sourceOffers) error {
		read := func() ([]sourceRSSItem, error) { return r.readRSSItems(raw) }
		if raw == sourceMailRSS {
			read = newsletter
		}
		return func(offers sourceOffers) error {
			items, err := read()
			if err == nil {
				r.collectRSSOffers(raw, kind, items, offers)
				if kind == "mail" {
					for _, item := range items {
						r.collectMailArchiveLinks(item, mailArchive)
					}
				}
			}
			return err
		}
	}
	type job struct {
		name string
		read func(sourceOffers) error
	}
	jobs := []job{
		{"Steam English", rss(sourceSteamRSS+"english", "steam")},
		{"Steam 简中", rss(sourceSteamRSS+"schinese", "steam")},
		{"Steam 繁中", rss(sourceSteamRSS+"tchinese", "steam")},
		{"Steam API", func(o sourceOffers) error { return r.steamAPI(o, steamArchive) }},
		{"Klei 客服", func(o sourceOffers) error { return r.supportRewards(supportLinks) }},
		{"Klei 邮件", rss(sourceMailRSS, "mail")},
		{"Klei 邮件存档", func(o sourceOffers) error { return r.archive(o, newsletter) }},
		{"Klei 更新 RSS", rss(sourceForumRSS, "release")},
		{"Klei PC 更新", func(o sourceOffers) error { return r.forum(o, sourceReleases, true) }},
		{"Klei 开发日志", func(o sourceOffers) error {
			return r.forum(o, sourceForum+"/forums/forum/72-dont-starve-together-developer-log/", false)
		}},
		{"Klei 综合讨论", func(o sourceOffers) error {
			return r.forum(o, sourceForum+"/forums/forum/66-dont-starve-together-general-discussion/", false)
		}},
		{"Klei Bluesky", r.social},
		{"DST 游戏内公告", r.gameNews},
	}
	type result struct {
		offers sourceOffers
		err    error
	}
	results := make([]result, len(jobs))
	queue := make(chan int, len(jobs))
	for i := range jobs {
		queue <- i
	}
	close(queue)
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				rows := sourceOffers{}
				err := jobs[i].read(rows)
				results[i] = result{rows, err}
			}
		}()
	}
	wg.Wait()
	var failures []string
	allOffers := sourceOffers{}
	for i, out := range results {
		if out.err != nil {
			label := jobs[i].name
			if errors.Is(out.err, errSourceBrowserChallenge) {
				label += "（" + errSourceBrowserChallenge.Error() + "）"
			} else if errors.Is(out.err, errForumScanLimit) {
				label += "（" + errForumScanLimit.Error() + "）"
			}
			failures = append(failures, label)
		}
		for code, points := range out.offers {
			allOffers.add(code, points)
		}
	}
	archive := sourceOffers{}
	for _, code := range accountOffers {
		archive.add(code, false)
	}
	for _, source := range []sourceOffers{steamArchive, mailArchive, supportLinks, articleLinks} {
		for code := range source {
			if _, recent := allOffers[code]; !recent {
				archive[code] = false
			}
		}
	}
	table, typeError := r.rewardTypes()
	if typeError == nil {
		_, days := table.missingPublications(allOffers, archive, r.now)
		if len(days) > 0 {
			if err := r.archivedReleasePoints(archive, days); err != nil {
				failures = append(failures, "Klei PC 历史更新")
			}
		}
		if err := r.archivedPromotionPoints(table, allOffers, archive); err != nil {
			failures = append(failures, "官方推广图存档")
		}
		if count, _ := table.missingPublications(allOffers, archive, r.now); count > 0 {
			failures = append(failures, fmt.Sprintf("%d 个点数链接待核对官方出处", count))
		}
	}
	if err := r.corroboratePoints(allOffers); err != nil {
		typeError = err
	}
	if err := r.listedActivePoints(archive); err == nil {
		for code, confirmed := range archive {
			if confirmed {
				allOffers.add(code, true)
			}
		}
	} else {
		typeError = err
	}
	if typeError != nil {
		failures = append(failures, "隐藏奖励种类核对")
	}
	var codes []string
	for code, points := range allOffers {
		if points || gifts {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	index := map[string]int{}
	unique := make([]Offer, 0, len(codes))
	for _, code := range codes {
		key := strings.ToLower(code)
		if i, ok := index[key]; ok {
			unique[i].Points = unique[i].Points || allOffers[code]
			continue
		}
		index[key] = len(unique)
		unique = append(unique, Offer{Code: code, Points: allOffers[code]})
	}
	if len(failures) > 0 {
		return unique, fmt.Errorf("以下公开来源未能完整检查：%s", strings.Join(failures, "、"))
	}
	return unique, nil
}

// Discover checks public announcements and corroborates offers from the account page.
func Discover(dataDir string, gifts bool, accountOffers ...string) ([]Offer, error) {
	dir := filepath.Join(dataDir, "public-source-cache")
	now := time.Now()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// URLs containing pagination cursors change. Expire the anonymous document cache.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, item := range entries {
			name := item.Name()
			if !item.Type().IsRegular() {
				continue
			}
			if !strings.HasPrefix(name, "source-") {
				if len(name) != 64 {
					continue
				}
				if _, err := hex.DecodeString(name); err != nil {
					continue
				}
			}
			if info, e := item.Info(); e == nil && now.Sub(info.ModTime()) > 48*time.Hour {
				_ = os.Remove(filepath.Join(dir, item.Name()))
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	reader := sourceReader{dir: dir, client: &http.Client{Timeout: 8 * time.Second}, ctx: ctx, now: now}
	return reader.readOffers(sourceFeedLocation(), gifts, accountOffers...)
}
