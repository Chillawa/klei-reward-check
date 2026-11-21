package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type activity struct {
	Last        int64  `json:"last"`
	Message     string `json:"message"`
	SourceIssue string `json:"source_issue,omitempty"`
	LoginNeeded bool   `json:"login_needed"`
	Points      *int64 `json:"points,omitempty"`
	PointsAt    int64  `json:"points_at,omitempty"`
}
type overview struct {
	preferences
	activity
	Saved    bool     `json:"saved"`
	Receipts []string `json:"receipts,omitempty"`
	Notice   string   `json:"notice,omitempty"`
}

func currentStatus(root string) (overview, error) {
	p, err := readPreferences(root)
	if err != nil {
		return overview{}, err
	}
	view := overview{preferences: p}
	if err := readState(stateFile(root, "native-status.json"), &view.activity); err != nil {
		return view, err
	}
	view.Saved = hasSession(root)
	history, err := rewardHistory(root)
	if err != nil {
		return view, err
	}
	for _, receipt := range history {
		if receipt.Points > 0 {
			view.Receipts = append(view.Receipts, fmt.Sprintf("%s　+%d 点", time.Unix(int64(receipt.At), 0).Format("2006-01-02"), receipt.Points))
		}
	}
	if !p.Enabled {
		if view.Saved {
			view.LoginNeeded = false
			view.Message = ""
			view.SourceIssue = ""
		} else {
			view.activity = activity{}
		}
	}
	// Older workers can persist a public-source diagnostic and an account
	// error together. Each errors.Join entry occupies its own line.
	lines := strings.Split(view.Message, "\n")
	view.Notice = strings.Join(slices.DeleteFunc(lines, func(line string) bool {
		return strings.HasPrefix(line, "以下公开来源未能完整检查：") || line == "未能核对 Klei 账户奖励页。"
	}), "\n")
	if view.SourceIssue != "" {
		view.Message = strings.TrimSpace(view.Message + "\n" + view.SourceIssue)
	}
	if p.Enabled && view.Points != nil {
		balance := fmt.Sprintf("余额 %d 点", *view.Points)
		if view.PointsAt > 0 && view.PointsAt != view.Last {
			balance += "（更新于 " + time.Unix(view.PointsAt, 0).Format("01-02 15:04") + "）"
		}
		if view.Last > 0 {
			balance = "上次检查 " + time.Unix(view.Last, 0).Format("01-02 15:04") + "，" + balance
		}
		view.Message = strings.TrimSpace(balance + "。\n" + view.Message)
	}
	if len(view.Receipts) > 5 {
		view.Receipts = view.Receipts[:5]
	}
	return view, nil
}
func ensureCurrentWorker(root string, schedule func(string, bool) error) error {
	p, err := readPreferences(root)
	if err != nil || !p.Enabled {
		return err
	}
	return locked(filepath.Dir(stateFile(root, "automatic.json")), "automatic.lock", func() error {
		p, err := readPreferences(root)
		if err != nil || !p.Enabled {
			return err
		}
		return schedule(root, true)
	})
}

func openStatus(root string) (overview, error) {
	err := ensureCurrentWorker(root, applySchedule)
	startPanelUpdate(root)
	view, readErr := currentStatus(root)
	if readErr != nil {
		return view, readErr
	}
	if err != nil && !errors.Is(err, errOperationBusy) {
		view.Message = err.Error()
		view.Notice = view.Message
	}
	return view, nil
}
