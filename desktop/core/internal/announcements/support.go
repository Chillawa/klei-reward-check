package announcements

import (
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

const sourceSupport = "https://support.klei.com/api/v2/help_center/en-us/articles/19628212085652.json"
const supportArticle = "https://support.klei.com/hc/en-us/articles/19628212085652-Incorrect-Reward-Redeem-Code-Sites"

func (r sourceReader) supportRewards(offers sourceOffers) error {
	data, err := r.exact(sourceSupport)
	if err != nil {
		return err
	}
	var doc struct {
		Article struct {
			ID           int64
			Locale, Body string
			Page         string `json:"html_url"`
		}
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		return err
	}
	item := doc.Article
	if item.ID != 19628212085652 || item.Locale != "en-us" || item.Page != supportArticle || item.Body == "" {
		return errors.New("Klei 客服文章与来源不匹配")
	}
	body, err := html.Parse(strings.NewReader(item.Body))
	if err != nil {
		return err
	}
	for _, a := range sourceNodes(body, func(n *html.Node) bool { return n.Data == "a" }) {
		url := sourceAttr(a, "href")
		if strings.TrimSpace(sourceText(a)) == url {
			offers.add(rewardLinkCode(url), false)
		}
	}
	return nil
}
