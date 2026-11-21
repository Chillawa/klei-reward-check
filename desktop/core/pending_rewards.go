package main

import (
	"maps"
	"sort"
	"strings"

	"klei-reward-check/desktop/internal/announcements"
)

// The background lock serializes queue updates. Only a terminal reward journal
// removes an offer, so a restart between submitting and recording it is safe.
func pendingRewardCodes(root string, offers []announcements.Offer, gifts bool) ([]string, error) {
	path := stateFile(root, "pending-rewards.json")
	pending := map[string]announcements.Offer{}
	if err := readState(path, &pending); err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, errRewardState
	}
	before := maps.Clone(pending)
	for _, offer := range offers {
		key := strings.ToLower(offer.Code)
		if existing, ok := pending[key]; !ok || offer.Points && !existing.Points {
			pending[key] = offer
		}
	}
	journals, journalErr := rewardJournals(root)
	for _, j := range journals {
		for key := range j.Seen {
			delete(pending, key)
		}
		if j.Stop != nil {
			code := strings.TrimPrefix(j.Stop.Reward, rewardOrigin+"/link/")
			delete(pending, strings.ToLower(code))
		}
	}
	var codes []string
	for key, offer := range pending {
		if !rewardCode.MatchString(offer.Code) || key != strings.ToLower(offer.Code) {
			return nil, errRewardState
		}
		if offer.Points || gifts {
			codes = append(codes, offer.Code)
		}
	}
	sort.Strings(codes)
	if !maps.Equal(before, pending) {
		if err := writeState(path, pending); err != nil {
			return nil, err
		}
	}
	return codes, journalErr
}
