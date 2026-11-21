package main

import (
	"golang.org/x/net/html"
	"net/url"
	"regexp"
	"strings"
)

var receiptLabels = map[string]string{
	"reward link claimed!":                  "CLAIMED",
	"reward link already claimed!":          "ALREADY_CLAIMED",
	"sorry, this link was already claimed.": "ALREADY_CLAIMED",
	"whoa! it looks like you already clicked this link before. you can only redeem it once.": "ALREADY_CLAIMED",
	"sorry, this link has expired.":                                            "EXPIRED",
	"this link doesn't exist. please check that you have copied it correctly.": "INVALID_LINK",
}
var rewardLinkErrors = map[string]string{"E_REWARD_LINK_CLAIMED": "ALREADY_CLAIMED", "E_REWARD_LINK_CLAIMED_BY_YOU": "ALREADY_CLAIMED", "E_REWARD_LINK_EXPIRED": "EXPIRED", "E_REWARD_LINK_INVALID": "INVALID_LINK"}
var receiptCurrency = regexp.MustCompile("^[0-9][0-9,]{0,8} (?:klei points?|spools?)$")
var receiptHiddenStyle = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden`)

func receiptVisible(element *html.Node) bool {
	switch element.Data {
	case "script", "style", "template", "noscript", "textarea", "title":
		return false
	}
	var id, class string
	for _, a := range element.Attr {
		if strings.HasPrefix(a.Key, "v-") || strings.HasPrefix(a.Key, ":") || strings.HasPrefix(a.Key, "@") {
			return false
		}
		switch a.Key {
		case "id":
			id = a.Val
		case "class":
			class = a.Val
		case "hidden":
			return false
		case "aria-hidden":
			if strings.EqualFold(a.Val, "true") {
				return false
			}
		case "style":
			if receiptHiddenStyle.MatchString(a.Val) {
				return false
			}
		}
	}
	return id == "kleiapp" || !strings.Contains(" "+strings.Join(strings.Fields(class), " ")+" ", " hidden ")
}

func htmlRewardReceipt(body string, status int) (string, string) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "UNCONFIRMED", ""
	}
	receipts := map[string]bool{}
	amounts := map[string]bool{}
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, bold bool) {
		if n.Type == html.ElementNode {
			if !receiptVisible(n) {
				return
			}
			bold = bold || n.Data == "b" || n.Data == "strong"
		} else if n.Type == html.TextNode {
			label := strings.ToLower(strings.Join(strings.Fields(n.Data), " "))
			if receipt := receiptLabels[label]; receipt != "" {
				receipts[receipt] = true
			}
			if strings.HasPrefix(label, "we're sorry but this reward is no longer active.") {
				receipts["EXPIRED"] = true
			}
			if bold && receiptCurrency.MatchString(label) {
				amounts[label] = true
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child, bold)
		}
	}
	visit(doc, false)
	amount := ""
	for k := range amounts {
		amount = k
	}
	// Conflicting amounts cannot corroborate a currency posting.
	if len(amounts) > 1 {
		amount = "CONFLICTING"
	}
	if len(receipts) == 1 && status < 500 {
		for receipt := range receipts {
			if receipt != "CLAIMED" || status == 200 {
				return receipt, amount
			}
		}
	}
	return "UNCONFIRMED", amount
}

func (c *rewardClient) claimOnce(code string) (string, string, error) {
	path := "/link/" + code
	page, err := c.request(path, true)
	if err != nil {
		return "", "", err
	}
	if loginResponse(page, path) {
		return "LOGIN_REQUIRED", "", nil
	}
	if next := accountRedirect(page, path); next != nil {
		if next.Path != "/account/rewards" {
			return "UNCONFIRMED", "", nil
		}
		if next.RawQuery != "" {
			values, err := url.ParseQuery(next.RawQuery)
			if err == nil && len(values) == 1 && len(values["error"]) == 1 {
				if receipt := rewardLinkErrors[values.Get("error")]; receipt != "" {
					return receipt, "", nil
				}
			}
			return "UNCONFIRMED", "", nil
		}
		path = "/account/rewards"
		page, err = c.request(path, false)
		if err != nil {
			return "", "", err
		}
	}
	if loginResponse(page, path) {
		return "LOGIN_REQUIRED", "", nil
	}
	if page.mime != "text/html" || page.status >= 300 && page.status < 400 {
		return "UNCONFIRMED", "", nil
	}
	receipt, amount := htmlRewardReceipt(string(page.body), page.status)
	return receipt, amount, nil
}
