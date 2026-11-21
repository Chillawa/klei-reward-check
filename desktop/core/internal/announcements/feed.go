package announcements

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The release workflow may embed the maintainer's feed. Its codes are hints:
// only original publications can supply an actionable offer.
var publicRewardFeedURL string

var sourceFeedNames = []string{
	"steam_rss", "steam_rss_schinese", "steam_rss_tchinese", "steam_api",
	"newsletter", "newsletter_archive", "klei_social_promo", "klei_pc_versions", "klei_forum_topics",
}

func (r sourceReader) feedHints(raw string) map[string]bool {
	if sourceURL(raw) == nil {
		return nil
	}
	data, err := r.exact(raw)
	if err != nil || len(data) > 524288 {
		return nil
	}
	var feed struct {
		Version   int
		CheckedAt time.Time `json:"checked_at"`
		Complete  bool      `json:"complete_scan"`
		Sources   []string
		Rewards   []struct {
			Code, Target string
			Evidence     []struct{ Source, URL, Date string }
		}
	}
	if json.Unmarshal(data, &feed) != nil || feed.Version != 1 || !feed.Complete || feed.Rewards == nil || len(feed.Sources) != len(sourceFeedNames) || r.now.Sub(feed.CheckedAt) > 3*time.Hour || feed.CheckedAt.After(r.now.Add(5*time.Minute)) {
		return nil
	}
	sources := map[string]bool{}
	for _, name := range feed.Sources {
		sources[name] = true
	}
	for _, name := range sourceFeedNames {
		if !sources[name] {
			return nil
		}
	}
	hints := map[string]bool{}
	for _, row := range feed.Rewards {
		if row.Code == "" || row.Target != "https://accounts.klei.com/link/"+row.Code || rewardLinkCode(row.Target) != row.Code || len(row.Evidence) == 0 {
			return nil
		}
		for _, e := range row.Evidence {
			if _, err := time.Parse("2006-01-02", e.Date); err != nil || !sources[e.Source] || sourceURL(e.URL) == nil {
				return nil
			}
			hints[e.URL] = true
		}
	}
	return hints
}

func sourceFeedLocation() string {
	if publicRewardFeedURL != "" {
		return strings.TrimSpace(publicRewardFeedURL)
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, path := range []string{filepath.Join(dir, "public-feed.txt"), filepath.Join(dir, "..", "Resources", "public-feed.txt")} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		data, e := io.ReadAll(io.LimitReader(f, 2049))
		f.Close()
		if e == nil && len(data) <= 2048 {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
