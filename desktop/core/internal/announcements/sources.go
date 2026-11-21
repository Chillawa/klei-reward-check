package announcements

import (
	"regexp"
)

const (
	sourceSteamRSS    = "https://store.steampowered.com/feeds/news/app/322330/?l="
	sourceSteamAPI    = "https://api.steampowered.com/ISteamNews/GetNewsForApp/v2/?appid=322330&count=100&maxlength=0&feeds=steam_community_announcements"
	sourceForum       = "https://kleiforums.com"
	sourceReleases    = sourceForum + "/game-updates/dst/"
	sourceForumRSS    = sourceForum + "/rss/6-dont-starve-together-updates.xml/"
	sourceCampaign    = "093864427d0ebb0ebe92ec38e"
	sourceMailRSS     = "https://us2.campaign-archive.com/feed?u=" + sourceCampaign + "&id=e4edaf9994"
	sourceMailArchive = "https://us2.campaign-archive.com/home/?u=" + sourceCampaign + "&id=e4edaf9994"
	sourceGiftArt     = "https://mcusercontent.com/" + sourceCampaign + "/images/6e39049c-2b35-6362-4cf7-a61f5c090b86.png"
	sourceDID         = "did:plc:bm3fpb7we4xj6sxumouyy4wk"
	sourceRPC         = "https://oyster.us-east.host.bsky.network/xrpc/com.atproto."
	sourcePosts       = sourceRPC + "repo.listRecords?repo=" + sourceDID + "&collection=app.bsky.feed.post&limit=12"
	sourceBodyLimit   = 4 << 20
	sourceAmount      = "[1-9][0-9]*(?:,[0-9]{3})*"
)

var (
	sourceEnglish        = regexp.MustCompile("(?i)^(?:(?:click here to )?(?:get|claim|collect|redeem|receive) (?:your )?)?(?:free (?:" + sourceAmount + " )?klei points?|(?:" + sourceAmount + " )?free klei points?|(?:" + sourceAmount + " )?klei points? for free)(?: here)?$")
	sourceChinese        = regexp.MustCompile("(?i)^(?:点击|點擊)?(?:这里|這裡|此处|此處)?(?:免费|免費)(?:领取|領取|获得|獲得|兌換|兑换)?\\s*(?:" + sourceAmount + "\\s*)?(?:个|個)?\\s*(?:科雷|克雷|Klei)\\s*(?:点数|點數|积分|積分)$")
	sourceLink           = regexp.MustCompile(`^/link/([A-Za-z0-9_-]{1,160})/?$`)
	sourceBB             = regexp.MustCompile(`(?is)\[url(?:=(?:"([^"]+)"|([^\]]+)))?\](.*?)\[/url\]`)
	sourceBBParagraph    = regexp.MustCompile(`(?i)\r?\n|\[/?p\]|\[br\s*/?\]`)
	sourceBBStyle        = regexp.MustCompile(`(?i)\[/?(?:b|i|u)\]`)
	sourceSteamArticle   = regexp.MustCompile(`^/news/app/322330/view/[0-9]+/?$`)
	sourceSteamCommunity = regexp.MustCompile(`^/(?:games|ogg)/322330/announcements/detail/[0-9]+/?$`)
	sourceTopic          = regexp.MustCompile(`^/forums/topic/[0-9]+-[-\p{L}\p{M}\p{N}_’]+/?$`)
	sourceRelease        = regexp.MustCompile(`^/game-updates/dst/[0-9]+-r[0-9]+/?$`)
	sourceMailSlug       = regexp.MustCompile(`^/kleientertainment/[A-Za-z0-9_-]+/?$`)
	sourceMailID         = regexp.MustCompile(`^[a-fA-F0-9]{10}$`)
	sourceShortMail      = regexp.MustCompile(`^/[A-Za-z0-9_-]+/?$`)
	sourceBuildDate      = regexp.MustCompile(`[0-9]+\s+(?:Release|Test)\s+Released\s+([0-9]+/[0-9]+/[0-9]+)`)
	sourceEditionDate    = regexp.MustCompile(`^([0-9]{2}/[0-9]{2}/[0-9]{4})\s+-\s+`)
	sourceRecordKey      = regexp.MustCompile(`^[a-z2-7]{13}$`)
	sourceGID            = regexp.MustCompile(`^[0-9]+$`)
)
