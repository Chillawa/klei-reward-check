package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var rewardCode = regexp.MustCompile("^[A-Za-z0-9_-]{1,160}$")

const kleiPoints = "KLEI_POINTS"
const kleiSpools = "SPOOLS"

type rewardCredit struct {
	Code   string
	Points int64
	At     float64
}
type rewardAttempt struct {
	Code          string           `json:"code"`
	Reward        string           `json:"reward"`
	Source        string           `json:"source"`
	Balance       map[string]int64 `json:"balance"`
	Anchor        *string          `json:"anchor"`
	EmptyAccount  string           `json:"empty_account,omitempty"`
	Started       float64          `json:"started"`
	Receipt       string           `json:"receipt,omitempty"`
	ReceiptAmount string           `json:"receipt_amount,omitempty"`
}
type rewardResult struct {
	Status       string `json:"status"`
	Reward       string `json:"reward"`
	Source       string `json:"source"`
	Receipt      string `json:"receipt,omitempty"`
	PointsBefore int64  `json:"points_before"`
	PointsAfter  int64  `json:"points_after"`
	PointsAdded  int64  `json:"points_added"`
	SpoolsAdded  int64  `json:"spools_added,omitempty"`
	ItemGranted  bool   `json:"item_granted,omitempty"`
}
type rewardJournal struct {
	Version    int                     `json:"version"`
	Active     *rewardAttempt          `json:"active"`
	Stop       *rewardResult           `json:"stop"`
	Seen       map[string]rewardResult `json:"seen"`
	CreditedAt float64                 `json:"credited_at,omitempty"`
	path       string
}

func negativeReceipt(s string) bool {
	return s == "ALREADY_CLAIMED" || s == "EXPIRED" || s == "INVALID_LINK"
}
func creditedReceipt(s string) bool {
	return s == "VERIFIED_POINTS" || s == "POINTS_POSTED_RECEIPT_MISSING"
}
func unixSeconds(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }
func digestText(s string) string      { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func (c *rewardClient) readData(path string) (map[string]any, error) {
	page, err := c.request(path, false)
	if err != nil {
		return nil, err
	}
	if loginResponse(page, path) || path == profileEndpoint && page.status == 403 {
		return nil, errLoginRequired
	}
	if page.status != 200 || page.mime != "application/json" {
		return nil, errRewardRead
	}
	var reply struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(page.body))
	decoder.UseNumber()
	if decoder.Decode(&reply) != nil || decoder.Decode(new(any)) != io.EOF || !reply.OK || reply.Data == nil {
		return nil, errRewardRead
	}
	return reply.Data, nil
}

func checkAccount(dataDir string) error { return openRewardClient(dataDir).checkAccount() }
func (c *rewardClient) checkAccount() error {
	data, err := c.readData(profileEndpoint)
	if err != nil {
		return err
	}
	name, _ := data["name"].(string)
	if strings.TrimSpace(name) == "" {
		return errRewardRead
	}
	return nil
}

func rewardCount(v any) (int64, bool) {
	var text string
	switch n := v.(type) {
	case string:
		text = n
	case json.Number:
		text = n.String()
	default:
		return 0, false
	}
	if text == "" || text[0] == '+' {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func rewardBalances(v any) (map[string]int64, error) {
	values, _ := v.(map[string]any)
	result := make(map[string]int64, 2)
	for _, currency := range []string{kleiPoints, kleiSpools} {
		n, ok := rewardCount(values[currency])
		if !ok || n < 0 {
			return nil, errRewardRead
		}
		result[currency] = n
	}
	return result, nil
}

type rewardSnapshot struct {
	balance map[string]int64
	ids     map[string]bool
	matches []map[string]any
}

func (c *rewardClient) readBalances() (map[string]int64, error) {
	items, err := c.readData(pointsEndpoint)
	if err != nil {
		return nil, err
	}
	return rewardBalances(items["Balance"])
}

func (c *rewardClient) snapshot(code string) (rewardSnapshot, error) {
	before, err := c.readBalances()
	if err != nil {
		return rewardSnapshot{}, err
	}
	ledger, err := c.readData(ledgerEndpoint)
	if err != nil {
		return rewardSnapshot{}, err
	}
	after, err := rewardBalances(ledger["Balances"])
	if err != nil {
		return rewardSnapshot{}, err
	}
	rows, ok := ledger["Transactions"].([]any)
	count, valid := rewardCount(ledger["TotalTransactions"])
	if !ok || !valid || count != int64(len(rows)) || before[kleiPoints] != after[kleiPoints] || before[kleiSpools] != after[kleiSpools] {
		return rewardSnapshot{}, errRewardRead
	}
	snap := rewardSnapshot{balance: before, ids: map[string]bool{}}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		id, ok := row["TxnID"].(string)
		if !ok {
			return rewardSnapshot{}, errRewardRead
		}
		if id != "" {
			snap.ids[digestText(id)] = true
		}
		name, _ := row["RewardLinkName"].(string)
		if strings.EqualFold(name, code) {
			snap.matches = append(snap.matches, row)
		}
	}
	return snap, nil
}

func loadRewardJournal(path string) (rewardJournal, error) {
	j := rewardJournal{path: path}
	if _, err := rewardFileInfo(path, false); err != nil {
		return j, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return j, err
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(body, &keys) != nil || keys["active"] == nil || keys["stop"] == nil || json.Unmarshal(body, &j) != nil || j.Version != 1 || j.Seen == nil {
		return j, errRewardState
	}
	if a := j.Active; a != nil {
		_, points := a.Balance[kleiPoints]
		_, spools := a.Balance[kleiSpools]
		if j.Stop != nil || !rewardCode.MatchString(a.Code) || a.Reward != rewardOrigin+"/link/"+a.Code || !points || !spools || a.Balance[kleiPoints] < 0 || a.Balance[kleiSpools] < 0 || a.Started <= 0 || a.Anchor == nil && a.EmptyAccount == "" {
			return j, errRewardState
		}
	}
	if stop := j.Stop; stop != nil && !creditedReceipt(stop.Status) && stop.Status != "NONPOINT_REWARD" {
		return j, errRewardState
	}
	for _, r := range j.Seen {
		if !negativeReceipt(r.Status) {
			return j, errRewardState
		}
	}
	return j, nil
}

func rewardJournals(dir string) ([]rewardJournal, error) {
	paths := []string{filepath.Join(dir, "klei.reward-verification.json")}
	manual := filepath.Join(dir, "manual-klei")
	if _, err := rewardFileInfo(manual, true); err == nil {
		entries, err := os.ReadDir(manual)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				paths = append(paths, filepath.Join(manual, e.Name()))
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var result []rewardJournal
	for i, path := range paths {
		j, err := loadRewardJournal(path)
		if i == 0 && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, nil
}

func (j *rewardJournal) save() error {
	tmp, err := os.CreateTemp(filepath.Dir(j.path), ".reward-journal-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(j)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return commitRewardFile(tmp.Name(), j.path)
}

func rewardHistory(dataDir string) ([]rewardCredit, error) {
	journals, err := rewardJournals(dataDir)
	if err != nil {
		return nil, err
	}
	var credits []rewardCredit
	for _, j := range journals {
		if j.Stop == nil || !creditedReceipt(j.Stop.Status) {
			continue
		}
		stamp := j.CreditedAt
		if stamp <= 0 {
			info, err := rewardFileInfo(j.path, false)
			if err != nil {
				return nil, err
			}
			stamp = unixSeconds(info.ModTime())
		}
		credits = append(credits, rewardCredit{Code: strings.TrimPrefix(j.Stop.Reward, rewardOrigin+"/link/"), Points: j.Stop.PointsAdded, At: stamp})
	}
	sort.SliceStable(credits, func(i, j int) bool { return credits[i].At > credits[j].At })
	return credits, nil
}

func (j *rewardJournal) finish(r rewardResult) (string, error) {
	a := j.Active
	r.Reward = a.Reward
	r.Source = a.Source
	r.Receipt = a.Receipt
	if r.Receipt == "" {
		r.Receipt = "UNAVAILABLE"
	}
	if negativeReceipt(r.Status) {
		j.Seen[strings.ToLower(a.Code)] = r
	} else {
		j.Stop = &r
	}
	j.Active = nil
	if err := j.save(); err != nil {
		return "", err
	}
	if creditedReceipt(r.Status) {
		return fmt.Sprintf("已到账 %d 点。", r.PointsAdded), nil
	}
	if r.Status == "NONPOINT_REWARD" {
		return "已领取神秘礼物。", nil
	}
	return "", nil
}

func hasRewardValues(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case string:
		return v != ""
	case json.Number:
		return v.String() != "0"
	default:
		return true
	}
}

func (c *rewardClient) reconcile(j *rewardJournal) (string, error) {
	a := j.Active
	snap, err := c.snapshot(a.Code)
	if err != nil {
		return "", err
	}
	if a.Anchor != nil {
		if !snap.ids[*a.Anchor] {
			return "", errAccountChanged
		}
	} else {
		key, err := accountCookieDigest(filepath.Join(c.dir, "klei.cookies"))
		if err != nil {
			return "", err
		}
		if a.EmptyAccount != key {
			return "", errAccountChanged
		}
	}
	pointsBefore, pointsAfter := a.Balance[kleiPoints], snap.balance[kleiPoints]
	noChange := pointsBefore == pointsAfter && a.Balance[kleiSpools] == snap.balance[kleiSpools]
	if len(snap.matches) == 0 && noChange {
		if negativeReceipt(a.Receipt) {
			return j.finish(rewardResult{Status: a.Receipt, PointsBefore: pointsBefore, PointsAfter: pointsAfter})
		}
		if a.Receipt == "LOGIN_REQUIRED" {
			j.Active = nil
			return "", j.save()
		}
	}
	if len(snap.matches) != 1 || negativeReceipt(a.Receipt) || a.Receipt == "LOGIN_REQUIRED" {
		return "", errRewardPending
	}
	row := snap.matches[0]
	when, ok := row["Time"].(json.Number)
	if !ok {
		return "", errRewardPending
	}
	stamp, err := when.Float64()
	kind, _ := row["Type"].(string)
	if err != nil || stamp < a.Started || stamp > unixSeconds(time.Now())+300 || !strings.HasPrefix(kind, "TXN_LINK_") || hasRewardValues(row["IsRollback"]) || hasRewardValues(row["CurrencyLosses"]) || hasRewardValues(row["ItemLosses"]) {
		return "", errRewardPending
	}
	totals := map[string]int64{kleiPoints: 0, kleiSpools: 0}
	gains, ok := row["CurrencyGains"].([]any)
	if !ok && row["CurrencyGains"] != nil {
		return "", errRewardPending
	}
	for _, value := range gains {
		gain, _ := value.(map[string]any)
		name, exists := gain["RawCurrencyName"]
		if !exists {
			name = gain["Name"]
		}
		currency, _ := name.(string)
		if _, ok := totals[currency]; !ok {
			return "", errRewardPending
		}
		count, ok := rewardCount(gain["Amount"])
		if !ok || count < 0 || count > math.MaxInt64-totals[currency] {
			return "", errRewardPending
		}
		totals[currency] += count
	}
	for k, n := range totals {
		if snap.balance[k]-a.Balance[k] != n {
			return "", errRewardPending
		}
	}
	if a.ReceiptAmount != "" {
		if !receiptCurrency.MatchString(a.ReceiptAmount) {
			return "", errRewardPending
		}
		fields := strings.Fields(a.ReceiptAmount)
		currency := kleiSpools
		if fields[1] == "klei" {
			currency = kleiPoints
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(fields[0], ",", ""), 10, 64)
		if err != nil || n != totals[currency] {
			return "", errRewardPending
		}
	}
	if totals[kleiPoints] > 0 {
		status := "POINTS_POSTED_RECEIPT_MISSING"
		if a.Receipt == "CLAIMED" {
			status = "VERIFIED_POINTS"
		}
		j.CreditedAt = stamp
		return j.finish(rewardResult{Status: status, PointsAdded: totals[kleiPoints], PointsBefore: pointsBefore, PointsAfter: pointsAfter})
	}
	items, ok := row["ItemGains"].([]any)
	if !ok && row["ItemGains"] != nil {
		return "", errRewardPending
	}
	if totals[kleiSpools] > 0 || len(items) > 0 {
		return j.finish(rewardResult{Status: "NONPOINT_REWARD", SpoolsAdded: totals[kleiSpools], ItemGranted: len(items) > 0})
	}
	return "", errRewardPending
}

func reconcilePending(dataDir string) (string, error) {
	journals, err := rewardJournals(dataDir)
	if err != nil {
		return "", err
	}
	for i := range journals {
		if journals[i].Active != nil {
			return openRewardClient(dataDir).reconcile(&journals[i])
		}
	}
	return "", nil
}

func claimReward(dataDir, code string) (string, error) { return openRewardClient(dataDir).claim(code) }
func (c *rewardClient) claim(code string) (string, error) {
	if !rewardCode.MatchString(code) {
		return "", errors.New("奖励码无效。")
	}
	journals, err := rewardJournals(c.dir)
	if err != nil {
		return "", err
	}
	for i := range journals {
		if journals[i].Active != nil {
			return c.reconcile(&journals[i])
		}
	}
	key := strings.ToLower(code)
	for _, j := range journals {
		if _, ok := j.Seen[key]; ok || j.Stop != nil && strings.EqualFold(j.Stop.Reward, rewardOrigin+"/link/"+code) {
			return "", nil
		}
	}
	snap, err := c.snapshot(code)
	if err != nil {
		return "", err
	}
	manual := filepath.Join(c.dir, "manual-klei")
	if err = os.Mkdir(manual, 0700); err == nil {
		err = syncRewardDirectory(c.dir)
	} else if os.IsExist(err) {
		_, err = rewardFileInfo(manual, true)
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(manual, digestText(key)+".json")
	j := rewardJournal{Version: 1, Seen: map[string]rewardResult{}, path: path}
	for _, existing := range journals {
		if existing.path == path {
			if existing.Stop != nil {
				return "", errRewardState
			}
			j = existing
			break
		}
	}
	a := &rewardAttempt{Code: code, Reward: rewardOrigin + "/link/" + code, Source: rewardOrigin + "/link/" + code, Balance: snap.balance, Started: unixSeconds(time.Now())}
	j.Active = a
	if len(snap.matches) > 0 {
		return j.finish(rewardResult{Status: "ALREADY_CLAIMED", PointsBefore: a.Balance[kleiPoints], PointsAfter: a.Balance[kleiPoints]})
	}
	var anchor string
	for id := range snap.ids {
		if anchor == "" || id < anchor {
			anchor = id
		}
	}
	if anchor != "" {
		a.Anchor = &anchor
	} else {
		a.EmptyAccount, err = accountCookieDigest(filepath.Join(c.dir, "klei.cookies"))
		if err != nil {
			return "", err
		}
	}
	if err = j.save(); err != nil {
		return "", err
	}
	a.Receipt, a.ReceiptAmount, err = c.claimOnce(code)
	if errors.Is(err, errClaimNotSent) {
		j.Active = nil
		if saveErr := j.save(); saveErr != nil {
			return "", saveErr
		}
		return "", err
	}
	if err != nil {
		return "", errRewardPending
	}
	if err = j.save(); err != nil {
		return "", err
	}
	if a.Receipt == "LOGIN_REQUIRED" {
		return "", errLoginRequired
	}
	return c.reconcile(&j)
}
