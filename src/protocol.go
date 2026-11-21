package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

type field []byte

func b(n protowire.Number, value []byte) field {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), value)
}
func v(n protowire.Number, value uint64) field {
	return protowire.AppendVarint(protowire.AppendTag(nil, n, protowire.VarintType), value)
}
func text(n protowire.Number, value string) field { return b(n, []byte(value)) }
func optionalText(n protowire.Number, value string) field {
	if value == "" {
		return nil
	}
	return text(n, value)
}
func message(fields ...field) []byte {
	var result []byte
	for _, f := range fields {
		result = append(result, f...)
	}
	return result
}

func checkHashRequest(hash []byte) []byte {
	return message(b(1, message(b(1, message(b(1, hash))), b(2, nil))))
}
func commitRequest(upload []byte, name string, hash []byte, model, makeName string, android int64, now time.Time) []byte {
	item := message(b(1, upload), text(2, name), b(3, hash),
		b(4, message(v(1, uint64(now.Unix())), v(2, 46000000))), v(7, 3), v(10, 1), v(17, 0))
	device := message(text(3, model), text(4, makeName), v(5, uint64(android)))
	return message(b(1, item), b(2, device), b(3, []byte{1, 3}))
}
func trashRequest(keys []string, apk, android int64) []byte {
	result := message(v(2, 1))
	for _, key := range keys {
		result = append(result, text(3, key)...)
	}
	options := message(b(2, nil), b(3, message(b(1, nil))), b(4, nil), b(5, message(b(1, nil))))
	version := message(v(1, uint64(apk)), text(2, strconv.FormatInt(android, 10)))
	return append(result, message(v(4, 1), b(8, message(b(4, options))), b(9, message(v(1, 5), b(2, version))))...)
}
func downloadRequest(key string) []byte {
	target := message(b(1, message(text(1, key))))
	options := message(b(1, message(b(7, message(b(2, nil))))),
		b(5, message(b(2, nil), b(3, nil), b(5, message(b(1, nil), v(3, 0))))))
	return message(b(1, target), b(2, options))
}

type wireValue struct {
	number uint64
	bytes  []byte
}
type wireMessage map[protowire.Number][]wireValue

func decode(data []byte) (wireMessage, error) {
	result := make(wireMessage)
	for len(data) != 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		var val wireValue
		switch typ {
		case protowire.VarintType:
			val.number, n = protowire.ConsumeVarint(data)
		case protowire.BytesType:
			val.bytes, n = protowire.ConsumeBytes(data)
		case protowire.Fixed32Type:
			var k uint32
			k, n = protowire.ConsumeFixed32(data)
			val.number = uint64(k)
		case protowire.Fixed64Type:
			val.number, n = protowire.ConsumeFixed64(data)
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
		}
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		result[num] = append(result[num], val)
		data = data[n:]
	}
	return result, nil
}
func (m wireMessage) first(n protowire.Number) wireValue {
	if vals := m[n]; len(vals) != 0 {
		return vals[0]
	}
	return wireValue{}
}
func (m wireMessage) str(n protowire.Number) string { return string(m.first(n).bytes) }
func (m wireMessage) num(n protowire.Number) int64  { return int64(m.first(n).number) }
func (m wireMessage) sub(path ...protowire.Number) wireMessage {
	for _, n := range path {
		var err error
		raw := m.first(n).bytes
		if raw == nil {
			return nil
		}
		m, err = decode(raw)
		if err != nil {
			return nil
		}
	}
	return m
}
func (m wireMessage) f32(n protowire.Number) float64 {
	return float64(math.Float32frombits(uint32(m.first(n).number)))
}
func (m wireMessage) f64(n protowire.Number) float64 {
	return math.Float64frombits(m.first(n).number)
}

type LibraryPage struct {
	State, Next string
	Items       []Media
	Deleted     []string
}

func parsePage(raw []byte) (LibraryPage, error) {
	top, err := decode(raw)
	if err != nil {
		return LibraryPage{}, err
	}
	page := top.sub(1)
	if page == nil {
		return LibraryPage{}, errors.New("missing library response")
	}
	result := LibraryPage{State: page.str(6), Next: page.str(1)}
	for _, entry := range page[2] {
		m, err := decode(entry.bytes)
		if err != nil {
			continue
		}
		if item, ok := parseMedia(m); ok {
			result.Items = append(result.Items, item)
		}
	}
	for _, entry := range page[9] {
		m, err := decode(entry.bytes)
		if err != nil {
			continue
		}
		if d := m.sub(1); d.num(1) == 1 {
			if key := d.sub(2).str(1); key != "" {
				result.Deleted = append(result.Deleted, key)
			}
		}
	}
	return result, nil
}

func parseMedia(m wireMessage) (Media, bool) {
	meta, kind := m.sub(2), m.sub(5)
	key := m.str(1)
	if key == "" || meta == nil || kind == nil {
		return Media{}, false
	}
	item := Media{
		Key: key, Name: meta.str(4), Caption: meta.str(3), Collection: meta.sub(1).str(1),
		Taken: meta.num(7), Zone: meta.num(8), Uploaded: meta.num(9), Size: meta.num(10),
		UploadStatus: meta.num(11), Version: meta.num(26), Dedup: meta.sub(21).str(1), Canonical: true,
		Quota: meta.sub(35).num(2), Original: meta.sub(35).num(3) == 2, Trashed: meta.sub(16).num(3),
		Archived: meta.sub(29).num(1) == 1, Favorite: meta.sub(31).num(1) == 1, Locked: meta.sub(39).num(1) == 1,
		Kind: kind.num(1),
	}
	if digest := meta.sub(13).first(1).bytes; len(digest) != 0 {
		item.SHA1 = hex.EncodeToString(digest)
		if item.Dedup == "" {
			item.Dedup = base64.RawURLEncoding.EncodeToString(digest)
		}
	}
	for _, p := range meta[5] {
		if prop, err := decode(p.bytes); err == nil && prop.num(1) == 27 {
			item.Canonical = false
			break
		}
	}
	if origin := meta.sub(30); origin != nil {
		item.Origin = "self"
		switch origin.num(1) {
		case 3:
			item.Origin = "partner"
		case 4:
			item.Origin = "shared"
		}
	}
	if photo := kind.sub(2); photo != nil {
		item.Edited = len(photo[4]) != 0
		main := photo.sub(1)
		item.RemoteURL = main.str(1)
		dim := main.sub(9)
		item.Width, item.Height = dim.num(1), dim.num(2)
		exif := dim.sub(5)
		item.Make, item.Model = exif.str(1), exif.str(2)
		item.Aperture, item.Shutter, item.ISO, item.Focal = exif.f32(4), exif.f32(5), exif.num(6), exif.f32(7)
	}
	if video := kind.sub(3); video != nil {
		if source := video.sub(2); source != nil {
			item.RemoteURL = source.str(1)
		}
		if dim := video.sub(4); dim != nil {
			item.Duration, item.Width, item.Height = dim.num(1), dim.num(4), dim.num(5)
		}
		rate := video.sub(6)
		item.CaptureFPS, item.EncodedFPS = rate.f64(4), rate.f64(5)
	}
	if motion := kind.sub(5, 2, 4); motion != nil {
		item.Motion, item.Duration, item.MotionWidth, item.MotionHeight = true, motion.num(1), motion.num(4), motion.num(5)
	}
	gps, place := m.sub(17, 1), m.sub(17, 5)
	item.Latitude = float64(int32(gps.num(1))) / 1e7
	item.Longitude = float64(int32(gps.num(2))) / 1e7
	item.Location, item.LocationID = place.sub(2).str(1), place.str(3)
	return item, true
}
