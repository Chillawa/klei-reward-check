package announcements

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/common"
	"github.com/makiuchi-d/gozxing/multi/qrcode"
	singleQR "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
)

type sourceSocialRecord struct {
	URI, CID string
	Value    struct {
		Type      string `json:"$type"`
		CreatedAt time.Time
		Text      string
		Embed     struct {
			Type   string `json:"$type"`
			Images []struct {
				Image struct {
					Type string `json:"$type"`
					Ref  struct {
						Link string `json:"$link"`
					}
					MimeType string
					Size     int
				}
			}
		}
		Facets []struct {
			Index    struct{ ByteStart, ByteEnd int }
			Features []struct {
				Type string `json:"$type"`
				URI  string
			}
		}
	}
}

func sourceCID(s string, codec byte) []byte {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	if !strings.HasPrefix(s, "b") || len(s) != 59 {
		return nil
	}
	raw, err := enc.DecodeString(strings.ToUpper(s[1:]))
	if err != nil || len(raw) != 36 || string(raw[:4]) != string([]byte{1, codec, 0x12, 0x20}) || "b"+strings.ToLower(enc.EncodeToString(raw)) != s {
		return nil
	}
	return raw[4:]
}
func socialPromotion(rec sourceSocialRecord) bool {
	v := rec.Value
	if sourcePoints(v.Text) {
		return true
	}
	text := strings.ToLower(v.Text)
	promotion := false
	for _, term := range []string{"pre order", "pre-order", "preorder", "gift", "reward", "calendar", "giveaway", "merch"} {
		if strings.Contains(text, term) {
			promotion = true
			break
		}
	}
	if !promotion {
		return false
	}
	for _, f := range v.Facets {
		start, end := f.Index.ByteStart, f.Index.ByteEnd
		if start < 0 || end <= start || end > len(v.Text) || !utf8.ValidString(v.Text[:start]) || !utf8.ValidString(v.Text[:end]) || len(f.Features) != 1 {
			continue
		}
		link := f.Features[0]
		u := sourceURL(link.URI)
		if link.Type != "app.bsky.richtext.facet#link" || u == nil {
			continue
		}
		switch u.Host {
		case "shop.klei.com":
			return true
		case "klei.gg", "www.klei.com", "klei.com":
			if u.Path != "" && u.Path != "/" {
				return true
			}
		}
	}
	return false
}

var errPhotoBudget = errors.New("本轮官方推广图片读取已达上限，尚有图片待查")

func (r sourceReader) photoOffer(rec sourceSocialRecord, index int, offers sourceOffers, remaining *int) error {
	v := rec.Value
	blob := v.Embed.Images[index].Image
	cid := blob.Ref.Link
	digest := sourceCID(cid, 0x55)
	if digest == nil || blob.Type != "blob" || (blob.MimeType != "image/png" && blob.MimeType != "image/jpeg") || blob.Size <= 0 || blob.Size > 2100000 {
		return errors.New("官方推广图片的格式、大小或 CID 无法核验")
	}
	raw := sourceRPC + "sync.getBlob?did=" + sourceDID + "&cid=" + cid
	data, _ := r.cached(raw, func(u *url.URL) bool { return u.String() == raw }, 48*time.Hour)
	if data == nil {
		if *remaining <= 0 {
			return errPhotoBudget
		}
		(*remaining)--
		var err error
		data, err = r.exact(raw)
		if err != nil {
			return err
		}
	}
	sum := sha256.Sum256(data)
	if len(data) != blob.Size || !bytes.Equal(sum[:], digest) {
		return errors.New("官方图片的长度或摘要校验失败")
	}
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+kind != blob.MimeType || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 4000000 {
		return errors.New("官方图片超出支持范围")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return err
	}
	hints := map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true}
	codes, err := qrcode.NewQRCodeMultiReader().DecodeMultiple(bitmap, hints)
	if len(codes) == 0 {
		if cid == adventPhotoCID {
			return printedPhotoOffer(img, offers)
		}
		single, singleErr := singleQR.NewQRCodeReader().Decode(bitmap, hints)
		if singleErr == nil {
			codes = []*gozxing.Result{single}
		} else {
			var notFound gozxing.NotFoundException
			if err == nil || !errors.As(singleErr, &notFound) {
				return errors.New("检测到官方推广图片二维码，但无法校验内容")
			}
			if !errors.As(err, &notFound) {
				return err
			}
		}
	}
	points := sourcePoints(v.Text) && len(v.Facets) == 0 && len(v.Embed.Images) == 1 && len(codes) == 1
	for _, code := range codes {
		offers.add(rewardLinkCode(code.GetText()), points)
	}
	return nil
}

const adventPhotoCID = "bafkreigvhujtxmbm54ii3ud3xzfdnpc4yvfnm5w4ae4g6srb5iik527xua"

// This verified original shows a 37-module printed QR at an angle. The four
// corners and horizontal sharpening compensate for its perspective and ink
// blur. Only the pixel data and QR checksum determine the payload.
func printedPhotoOffer(img image.Image, offers sourceOffers) error {
	const size = 37
	transform := common.PerspectiveTransform_QuadrilateralToQuadrilateral(0, 0, size, 0, size, size, 0, size, 684.90, 550.87, 799.10, 553.17, 799.93, 768.69, 687.30, 795.56)
	blue := func(x, y int) float64 {
		_, _, b, _ := img.At(x, y).RGBA()
		return float64(b >> 8)
	}
	sample := func(x, y float64) float64 {
		xy := []float64{x, y}
		transform.TransformPoints(xy)
		px, py := int(xy[0]), int(xy[1])
		fx, fy := xy[0]-float64(px), xy[1]-float64(py)
		return (blue(px, py)*(1-fx)+blue(px+1, py)*fx)*(1-fy) + (blue(px, py+1)*(1-fx)+blue(px+1, py+1)*fx)*fy
	}
	var grid [size][size]float64
	for row := range size {
		for col := range size {
			x, y := float64(col)+.5, float64(row)+.5
			grid[row][col] = 4.2421*sample(x, y) - 1.6347*sample(x+.5, y) - .9226*sample(x-.5, y)
		}
	}
	bits, _ := gozxing.NewSquareBitMatrix(size)
	for row := range size {
		for col := range size {
			var background float64
			for j := -3; j <= 3; j++ {
				for i := -3; i <= 3; i++ {
					background += grid[max(0, min(size-1, row+j))][max(0, min(size-1, col+i))]
				}
			}
			if grid[row][col]-background/49 < 25.5 {
				bits.Set(col, row)
			}
		}
	}
	result, err := decoder.NewDecoder().Decode(bits, nil)
	if err != nil {
		return errors.New("官方推广图片的印刷二维码无法校验")
	}
	offers.add(rewardLinkCode(result.GetText()), false)
	return nil
}
func socialRecordKey(rec sourceSocialRecord) string {
	key, found := strings.CutPrefix(rec.URI, "at://"+sourceDID+"/app.bsky.feed.post/")
	if !found || !sourceRecordKey.MatchString(key) || rec.Value.Type != "app.bsky.feed.post" || sourceCID(rec.CID, 0x71) == nil {
		return ""
	}
	return key
}

func (r sourceReader) verifySocialPublisher() error {
	raw := sourceRPC + "repo.describeRepo?repo=" + sourceDID
	data, err := r.exact(raw)
	if err != nil {
		return err
	}
	var id struct {
		Did, Handle     string
		HandleIsCorrect bool
		DidDoc          struct {
			ID          string
			AlsoKnownAs []string
			Service     []struct{ ID, Type, ServiceEndpoint string }
		}
	}
	if err = json.Unmarshal(data, &id); err != nil {
		return err
	}
	const handle = "kleientertainment.bsky.social"
	validAlias := false
	validPDS := false
	for _, a := range id.DidDoc.AlsoKnownAs {
		if a == "at://"+handle {
			validAlias = true
		}
	}
	for _, s := range id.DidDoc.Service {
		if (s.ID == "#atproto_pds" || s.ID == sourceDID+"#atproto_pds") && s.Type == "AtprotoPersonalDataServer" && s.ServiceEndpoint == "https://oyster.us-east.host.bsky.network" {
			validPDS = true
		}
	}
	if !validAlias || !validPDS || !id.HandleIsCorrect || id.Handle != handle || id.Did != sourceDID || id.DidDoc.ID != sourceDID {
		return errors.New("Bluesky 官方发布者身份不匹配")
	}
	return nil
}

func (r sourceReader) social(offers sourceOffers) error {
	if err := r.verifySocialPublisher(); err != nil {
		return err
	}
	raw := sourcePosts
	var gaps []error
	remainingPhotos := 4
	var budgetReached bool
	for page := 0; page < 3; page++ {
		data, err := r.exact(raw)
		if err != nil {
			return err
		}
		var posts struct {
			Records []sourceSocialRecord
			Cursor  string
		}
		if err = json.Unmarshal(data, &posts); err != nil {
			return err
		}
		if posts.Records == nil {
			return errors.New("Bluesky 记录字段缺失")
		}
		recent := false
		for _, rec := range posts.Records {
			v := rec.Value
			key := socialRecordKey(rec)
			if key == "" || !r.recent(v.CreatedAt) {
				continue
			}
			if v.Embed.Type == "app.bsky.embed.images" && socialPromotion(rec) {
				if len(v.Embed.Images) < 1 || len(v.Embed.Images) > 4 {
					gaps = append(gaps, fmt.Errorf("%s 官方推广图片数量异常", key))
				} else {
					for i := range v.Embed.Images {
						e := r.photoOffer(rec, i, offers, &remainingPhotos)
						if errors.Is(e, errPhotoBudget) {
							budgetReached = true
						} else if e != nil {
							gaps = append(gaps, fmt.Errorf("%s 图片 %d：%w", key, i+1, e))
						}
					}
				}
			}
			recent = true
			// Read only links in the author’s own text or facets.
			last := strings.LastIndex(v.Text, "https://accounts.klei.com/link/")
			if len(v.Facets) == 0 && last >= 0 {
				code := rewardLinkCode(strings.TrimSpace(v.Text[last:]))
				if code != "" && sourcePoints(strings.TrimRight(v.Text[:last], " ：:\n")) {
					offers.add(code, true)
				}
			}
			if len(v.Facets) != 1 {
				continue
			}
			for _, f := range v.Facets {
				if len(f.Features) != 1 {
					continue
				}
				start, end := f.Index.ByteStart, f.Index.ByteEnd
				if start < 0 || end <= start || end > len(v.Text) || !utf8.ValidString(v.Text[:start]) || !utf8.ValidString(v.Text[:end]) {
					continue
				}
				for _, feature := range f.Features {
					if feature.Type != "app.bsky.richtext.facet#link" {
						continue
					}
					code := rewardLinkCode(feature.URI)
					label := v.Text
					if strings.TrimSpace(v.Text[end:]) == "" && v.Text[start:end] == feature.URI {
						label = strings.TrimRight(v.Text[:start], " ：:\n")
					}
					if code != "" && sourcePoints(label) && (v.Text[start:end] == feature.URI || start == 0 && end == len(v.Text)) {
						offers.add(code, true)
					}
				}
			}
		}
		if !recent || posts.Cursor == "" {
			break
		}
		if !sourceRecordKey.MatchString(posts.Cursor) || page == 2 {
			gaps = append(gaps, errors.New("Bluesky 目录仍有后续记录待查"))
			break
		}
		raw = sourcePosts + "&cursor=" + url.QueryEscape(posts.Cursor)
	}
	if budgetReached {
		gaps = append(gaps, errPhotoBudget)
	}
	return errors.Join(gaps...)
}
