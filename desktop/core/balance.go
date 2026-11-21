package main

import (
	"path/filepath"
	"time"
)

// Old status files timestamped the balance and the background scan together.
func (a *activity) retainNewerPoints(previous activity) {
	if previous.Points == nil {
		return
	}
	observed := previous.PointsAt
	if observed == 0 {
		observed = previous.Last
	}
	if a.Points == nil || observed > a.PointsAt {
		a.Points = previous.Points
		a.PointsAt = observed
	}
}

func (account *rewardClient) refreshPoints() error {
	return locked(account.dir, "klei.lock", func() error {
		balances, err := account.readBalances()
		if err != nil {
			return err
		}
		path := stateFile(account.dir, "native-status.json")
		return locked(filepath.Dir(path), "automatic.lock", func() error {
			var status activity
			if err := readState(path, &status); err != nil {
				return err
			}
			points := balances[kleiPoints]
			status.Points, status.PointsAt = &points, time.Now().Unix()
			return writeState(path, status)
		})
	})
}
