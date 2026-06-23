package server

import (
	"strings"
	"testing"
)

func TestEncodeMinIOTags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want map[string]string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"key-value pairs", []string{"vendor:UIA", "mime:application/pdf"},
			map[string]string{"vendor": "UIA", "mime": "application/pdf"}},
		{"bare tags packed under tag1..tagN", []string{"anatomy", "diagram", "reference"},
			map[string]string{"tag1": "anatomy", "tag2": "diagram", "tag3": "reference"}},
		{"mixed", []string{"vendor:UIA", "anatomy", "mime:application/pdf"},
			map[string]string{"vendor": "UIA", "tag1": "anatomy", "mime": "application/pdf"}},
		{"duplicate keys keep first", []string{"vendor:UIA", "vendor:OTHER"},
			map[string]string{"vendor": "UIA"}},
		{"whitespace trimmed", []string{"  vendor : UIA  ", "  bare  "},
			map[string]string{"vendor": "UIA", "tag1": "bare"}},
		{"empty entries skipped", []string{"", "   ", "good"},
			map[string]string{"tag1": "good"}},
		{"trailing colon treated as bare", []string{"halfbroken:"},
			map[string]string{"tag1": "halfbroken:"}},
		{"leading colon treated as bare", []string{":halfbroken"},
			map[string]string{"tag1": ":halfbroken"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeMinIOTags(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q (full got=%v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestEncodeMinIOTags_CapsAtTen(t *testing.T) {
	in := make([]string, 15)
	for i := range in {
		in[i] = strings.Repeat("a", 1) + string(rune('0'+i%10)) // unique bare tags
	}
	got := encodeMinIOTags(in)
	if len(got) > 10 {
		t.Errorf("encoded %d tags; S3 caps at 10", len(got))
	}
}

func TestEncodeMinIOTags_DropsInvalidChars(t *testing.T) {
	// Newlines, control chars, etc. fail S3's validTagKeyValue regex
	// and would 400 from MinIO at upload time. Drop them locally.
	in := []string{"good:value", "bad\nkey:value", "bad:val\tue", "x\x00:y"}
	got := encodeMinIOTags(in)
	if v, ok := got["good"]; !ok || v != "value" {
		t.Errorf("good tag dropped: got=%v", got)
	}
	for k := range got {
		if k != "good" {
			t.Errorf("invalid-char tag accepted: %q -> %q", k, got[k])
		}
	}
}
