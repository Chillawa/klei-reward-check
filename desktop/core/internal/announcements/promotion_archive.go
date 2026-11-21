package announcements

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const promotionArchive = sourceRPC + "repo.listRecords?repo=" + sourceDID + "&collection=app.bsky.feed.post&limit=100"

// A sale may appear in the feed before launch or be promoted again the following month.
func promotionMonthMatches(posted, month time.Time) bool {
	return !posted.Before(month.AddDate(0, 0, -7)) && posted.Before(month.AddDate(0, 2, 0))
}

func (r sourceReader) archivedPromotionPoints(table wikiRewardTable, recent, archive sourceOffers) error {
	missing := map[string]time.Time{}
	for raw, entry := range table.byURL {
		if !entry.active || !entry.pointsOnly || entry.photoMonth.IsZero() || entry.photoMonth.After(r.now) {
			continue
		}
		code := rewardLinkCode(raw)
		_, inRecent := recent[code]
		_, inArchive := archive[code]
		if code != "" && !inRecent && !inArchive {
			missing[code] = entry.photoMonth
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := r.verifySocialPublisher(); err != nil {
		return err
	}
	data, err := r.exact(promotionArchive)
	if err != nil {
		return err
	}
	var page struct{ Records []sourceSocialRecord }
	if err := json.Unmarshal(data, &page); err != nil {
		return err
	}
	if page.Records == nil {
		return errors.New("Bluesky 存档记录字段缺失")
	}

	type picture struct {
		record *sourceSocialRecord
		index  int
	}
	var photos, ordered []picture
	for i := range page.Records {
		rec := &page.Records[i]
		v := &rec.Value
		if socialRecordKey(*rec) == "" || v.CreatedAt.IsZero() || v.CreatedAt.After(r.now) || v.Embed.Type != "app.bsky.embed.images" || len(v.Embed.Images) > 4 || !socialPromotion(*rec) {
			continue
		}
		for _, month := range missing {
			if !promotionMonthMatches(v.CreatedAt, month) {
				continue
			}
			for j, image := range v.Embed.Images {
				p := picture{rec, j}
				if image.Image.Ref.Link == adventPhotoCID {
					ordered = append(ordered, p)
				} else {
					photos = append(photos, p)
				}
			}
			break
		}
	}
	// Rotate other photos across scans so early posts cannot consume every attempt.
	if len(photos) > 0 {
		now := r.now.UTC()
		start := int((now.Unix()/86400 + int64(now.Hour())) % int64(len(photos)))
		ordered = append(ordered, photos[start:]...)
		ordered = append(ordered, photos[:start]...)
	}
	var gaps []error
	remaining := 2
	for _, p := range ordered {
		found := sourceOffers{}
		if err := r.photoOffer(*p.record, p.index, found, &remaining); err != nil {
			gaps = append(gaps, fmt.Errorf("%s 图片 %d：%w", socialRecordKey(*p.record), p.index+1, err))
			if errors.Is(err, errPhotoBudget) {
				break
			}
			continue
		}
		for code := range found {
			if month, wanted := missing[code]; wanted && promotionMonthMatches(p.record.Value.CreatedAt, month) {
				archive.add(code, false)
				delete(missing, code)
			}
		}
		if len(missing) == 0 {
			return nil
		}
	}
	return errors.Join(gaps...)
}
