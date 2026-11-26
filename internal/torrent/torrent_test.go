package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

func TestMagnet(t *testing.T) {
	for _, tc := range []struct{ link, hash, err string }{
		{"magnet:?xt=urn:btih:C9E15763F722F23E98A29DECDFAE341B98D53056&dn=Show.S01E01", "c9e15763f722f23e98a29decdfae341b98d53056", ""},
		{"magnet:?xt=urn:btih:ZHQVOY7XELZD5GFCTXWN7LRUDOMNKMCW", "c9e15763f722f23e98a29decdfae341b98d53056", ""},
		{"magnet:?xt=urn:btmh:1220abcd&dn=v2", "", "no v1 info hash"},
		{"https://example.invalid/a.torrent", "", "not a magnet link"},
		{"magnet:?xt=urn:btih:nothex", "", "no v1 info hash"},
	} {
		m, err := Magnet(tc.link)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: error %v, want %q", tc.link, err, tc.err)
			}
			continue
		}
		if err != nil || m.Hash != tc.hash {
			t.Errorf("%s: %+v, %v", tc.link, m, err)
		}
	}
}

func TestFile(t *testing.T) {
	info := "d6:lengthi5e4:name8:Film.mkv12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	data := []byte("d8:announce3:url4:info" + info + "e")
	sum := sha1.Sum([]byte(info))
	m, err := File(data)
	if err != nil || m.Hash != hex.EncodeToString(sum[:]) || m.Name != "Film.mkv" {
		t.Fatalf("%+v, %v", m, err)
	}

	v2 := []byte("d4:infod9:file treede12:meta versioni2e4:name1:xee")
	if _, err := File(v2); err == nil || !strings.Contains(err.Error(), "v2-only") {
		t.Errorf("v2-only: %v", err)
	}
	for _, bad := range []string{
		"", "plain words", "d4:info", "d4:infoi1ee", "d4:infod4:name1:xee", // no pieces
		"d4:info" + info + "e" + "trailing", "d9999999999:x", "d4:infod4:name-1:ee",
		strings.Repeat("l", 100) + strings.Repeat("e", 100),
	} {
		if _, err := File([]byte(bad)); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

func FuzzFile(f *testing.F) {
	f.Add([]byte("d4:infod6:pieces20:aaaaaaaaaaaaaaaaaaaa4:name1:xee"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = File(b) })
}
