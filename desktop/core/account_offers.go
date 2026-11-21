package main

import "klei-reward-check/desktop/internal/announcements"

func (c *rewardClient) readAccountOffers() ([]string, error) {
	const path = "/account/rewards"
	page, err := c.request(path, false)
	if err != nil {
		return nil, err
	}
	if loginResponse(page, path) {
		return nil, errLoginRequired
	}
	if page.status == 200 && page.mime == "text/html" {
		if codes, ok := announcements.AccountOffers(page.body); ok {
			return codes, nil
		}
	}
	return nil, errRewardRead
}
