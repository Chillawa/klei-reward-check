package main

import (
	"errors"
	"path/filepath"
)

type preferences struct {
	Enabled  bool   `json:"enabled"`
	Gifts    bool   `json:"include_gifts"`
	Revision uint64 `json:"revision,omitempty"`
}

func readPreferences(root string) (p preferences, err error) {
	err = readState(stateFile(root, "automatic.json"), &p)
	return
}

type settingsActions struct {
	login, check func(string) error
	reconcile    func(string) (string, error)
	schedule     func(string, bool) error
}

func updateSettings(root, action string, gifts bool) error {
	if action == "balance" {
		return openRewardClient(root).refreshPoints()
	}
	return (settingsActions{captureLogin, checkAccount, reconcilePending, applySchedule}).change(root, action, gifts)
}

func (hooks settingsActions) change(root, action string, gifts bool) error {
	if err := ensurePrivateDir(root); err != nil {
		return err
	}
	dir := filepath.Dir(stateFile(root, "automatic.json"))
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	path := stateFile(root, "automatic.json")
	if action == "stop" || action == "gifts" {
		return locked(dir, "automatic.lock", func() error {
			p, err := readPreferences(root)
			if err != nil {
				return err
			}
			p.Revision++
			if action == "gifts" {
				p.Gifts = gifts
				return writeState(path, p)
			}
			// A running job observes this flag before starting another reward, even if
			// the operating system cannot remove its scheduled task.
			p.Enabled = false
			if err = writeState(path, p); err != nil {
				return err
			}
			return hooks.schedule(root, false)
		})
	}
	if action != "login" && action != "start" {
		return errors.New("未知操作。")
	}
	return locked(root, "native-login.lock", func() error {
		initial, err := readPreferences(root)
		if err != nil {
			return err
		}
		if action == "start" && initial.Enabled {
			return ensureCurrentWorker(root, hooks.schedule)
		}
		status := activity{}
		// The account lock covers credentials and journals. Preference changes and
		// stopping the task remain available throughout interactive authentication.
		err = locked(root, "klei.lock", func() error {
			if action == "start" && hasSession(root) {
				if err := hooks.check(root); !errors.Is(err, errLoginRequired) {
					return err
				}
			}
			if err := hooks.login(root); err != nil {
				return err
			}
			if err := hooks.check(root); err != nil {
				return err
			}
			if action == "login" {
				msg, err := hooks.reconcile(root)
				if errors.Is(err, errAccountChanged) || err != nil && !errors.Is(err, errRewardPending) {
					return err
				}
				if err != nil {
					msg = err.Error()
				}
				status.Message = msg
			}
			return nil
		})
		if err != nil {
			return err
		}
		return locked(dir, "automatic.lock", func() error {
			current, err := readPreferences(root)
			if err != nil {
				return err
			}
			if current != initial {
				return errors.New("设置已更改，请重新确认。")
			}
			next := current
			next.Enabled = true
			next.Gifts = gifts
			next.Revision++
			if err = writeState(path, next); err != nil {
				return err
			}
			if err = hooks.schedule(root, true); err != nil {
				if rollback := writeState(path, current); rollback != nil {
					return errors.Join(err, rollback)
				}
				return err
			}
			return writeState(stateFile(root, "native-status.json"), status)
		})
	})
}
