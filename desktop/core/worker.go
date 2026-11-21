package main

import (
	"errors"
	"path/filepath"
	"time"

	"klei-reward-check/desktop/internal/announcements"
)

func needsAccountLogin(err error) bool {
	return errors.Is(err, errLoginRequired) || errors.Is(err, errAccountChanged)
}

func runWorker(root string) error {
	dir := filepath.Dir(stateFile(root, "automatic.json"))
	p, err := readPreferences(root)
	if err != nil || !p.Enabled {
		return err
	}
	err = locked(dir, "background.lock", func() error {
		var notices []string
		// Recheck the consent and release the account lock between rewards, so a
		// scan cannot block stopping the service or monopolize a sign-in request.
		withAccount := func(operation func() (string, error)) (bool, error) {
			allowed := false
			err := locked(root, "klei.lock", func() error {
				now, err := readPreferences(root)
				if err != nil || now != p {
					return err
				}
				allowed = true
				msg, err := operation()
				if msg != "" {
					notices = append(notices, msg)
				}
				return err
			})
			return allowed, err
		}
		account := openRewardClient(root)
		var accountCodes []string
		var accountPageErr error
		allowed, checkErr := withAccount(func() (string, error) {
			accountCodes, accountPageErr = account.readAccountOffers()
			if errors.Is(accountPageErr, errLoginRequired) {
				return "", accountPageErr
			}
			if accountPageErr != nil {
				// A changed rewards template must not disable independently published offers.
				if err := account.checkAccount(); err != nil {
					return "", err
				}
			}
			return reconcilePending(root)
		})
		var codes []string
		var scanErr, blockingScanErr error
		if allowed {
			var offers []announcements.Offer
			offers, scanErr = announcements.Discover(root, p.Gifts, accountCodes...)
			// Usable offers are processed even if one public feed is unavailable.
			// The status file preserves that feed's error for the next check.
			if len(offers) == 0 {
				blockingScanErr = scanErr
			}
			if accountPageErr != nil {
				scanErr = errors.Join(scanErr, errors.New("未能核对 Klei 账户奖励页。"))
				blockingScanErr = errors.Join(blockingScanErr, accountPageErr)
			}
			// Keep public discoveries even when the account is offline or needs to sign in.
			var queueErr error
			codes, queueErr = pendingRewardCodes(root, offers, p.Gifts)
			checkErr = errors.Join(checkErr, queueErr)
		}
		for _, code := range codes {
			if checkErr != nil || !allowed {
				break
			}
			allowed, checkErr = withAccount(func() (string, error) { return claimReward(root, code) })
		}
		if len(codes) > 0 && allowed && checkErr == nil {
			_, checkErr = pendingRewardCodes(root, nil, p.Gifts)
		}
		var points *int64
		var pointsAt int64
		if allowed && checkErr == nil {
			_, checkErr = withAccount(func() (string, error) {
				balances, err := account.readBalances()
				if err == nil {
					current := balances[kleiPoints]
					points = &current
					pointsAt = time.Now().Unix()
				}
				return "", err
			})
		}
		if errors.Is(checkErr, errOperationBusy) {
			return nil
		}
		relogin := false
		err := locked(dir, "automatic.lock", func() error {
			now, err := readPreferences(root)
			if err != nil || now != p {
				return err
			}
			var prev activity
			path := stateFile(root, "native-status.json")
			if err := readState(path, &prev); err != nil {
				return err
			}
			record := activity{Last: time.Now().Unix(), Points: points, PointsAt: pointsAt}
			if scanErr != nil {
				record.SourceIssue = scanErr.Error()
			}
			if checkErr != nil {
				record.Message = checkErr.Error()
				record.LoginNeeded = needsAccountLogin(checkErr)
			} else if len(notices) > 0 {
				record.Message = notices[len(notices)-1]
			}
			if record.LoginNeeded {
				relogin = !prev.LoginNeeded
			}
			if !record.LoginNeeded {
				record.retainNewerPoints(prev)
			}
			return writeState(path, record)
		})
		if err != nil && !errors.Is(err, errOperationBusy) {
			return err
		}
		for _, msg := range notices {
			notifyUser(msg)
		}
		if relogin {
			notifyUser("请打开 Klei 点数自动领重新登录。")
		}
		return errors.Join(checkErr, blockingScanErr)
	})
	if errors.Is(err, errOperationBusy) {
		return nil
	}
	return err
}
