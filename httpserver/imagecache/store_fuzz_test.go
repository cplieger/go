package imagecache

import (
	"crypto/sha256"
	"testing"
	"time"
)

// FuzzHeader_roundTrip holds that a cache file's header decodes to what was
// encoded, and that the file cut short at any byte is refused.
func FuzzHeader_roundTrip(f *testing.F) {
	f.Add("radarr-1", "42", "poster-250.jpg", uint8(0), int64(946684800e9), []byte("\xFF\xD8\xFF"))
	f.Add("", "", "", uint8(3), int64(-1), []byte{})
	f.Add("a\x00/..", "ポスター", string(make([]byte, 128)), uint8(1), int64(1)<<62, make([]byte, 300))
	types := []string{"image/jpeg", "image/png", "image/gif", "image/webp"}
	f.Fuzz(func(t *testing.T, space, id, file string, typ uint8, ns int64, data []byte) {
		if len(space) > maxPart || len(id) > maxPart || len(file) > maxPart {
			t.Skip()
		}
		data = data[:min(len(data), 64)]
		h := header{
			key:     Key{Space: space, ID: id, File: file},
			typ:     types[int(typ)%len(types)],
			fetched: time.Unix(0, ns),
			n:       int64(len(data)),
			sum:     sha256.Sum256(data),
		}
		head := appendHeader(nil, &h)
		whole := append(head, data...)
		got, hlen, err := parseHeader(whole[:min(len(whole), headerCap)], int64(len(whole)))
		if err != nil {
			t.Fatalf("parseHeader: %v", err)
		}
		if got.key != h.key || got.typ != h.typ || !got.fetched.Equal(h.fetched) || got.sum != h.sum || got.n != h.n || hlen != len(head) {
			t.Fatalf("decoded %+v (%d), want %+v (%d)", got, hlen, h, len(head))
		}
		for cut := range whole {
			if _, _, err := parseHeader(whole[:min(cut, headerCap)], int64(cut)); err == nil {
				t.Fatalf("a file cut to %d of %d bytes was accepted", cut, len(whole))
			}
		}
	})
}
