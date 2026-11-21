package announcements

import (
	"encoding/json"
	"errors"
	"html"
	"time"
)

func (r sourceReader) steamAPI(offers, archive sourceOffers) error {
	data, err := r.exact(sourceSteamAPI)
	if err != nil {
		return err
	}
	var result struct {
		AppNews struct {
			AppID     int
			NewsItems []struct {
				AppID                   int
				FeedName, Contents, GID string
				Date                    int64
			}
		}
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	if result.AppNews.AppID != 322330 || len(result.AppNews.NewsItems) == 0 {
		return errors.New("Steam 公告缺少游戏标识或正文")
	}
	for _, item := range result.AppNews.NewsItems {
		if item.AppID != 322330 || item.FeedName != "steam_community_announcements" || !sourceGID.MatchString(item.GID) {
			continue
		}
		published := time.Unix(item.Date, 0)
		if item.Date <= 0 || published.After(r.now.Add(5*time.Minute)) {
			continue
		}
		if r.recent(published) {
			offers.fromBB(item.Contents)
		}
		for _, match := range sourceBB.FindAllStringSubmatch(html.UnescapeString(item.Contents), -1) {
			archive.add(bbRewardCode(match), false)
		}
	}
	return nil
}
