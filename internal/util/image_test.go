package util

import (
	"encoding/base64"
	"testing"
)

func TestDetectImageMimeType(t *testing.T) {
	t.Parallel()

	pngHeader := base64.StdEncoding.EncodeToString([]byte("\x89PNG\x0D\x0A\x1A\x0A" + "\x00\x00\x00\x0DIHDR" + string(make([]byte, 100))))
	jpegHeader := base64.StdEncoding.EncodeToString([]byte("\xFF\xD8\xFF\xE0" + string(make([]byte, 100))))
	webpHeader := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WEBPVP8 " + string(make([]byte, 100))))
	gifHeader := base64.StdEncoding.EncodeToString([]byte("GIF89a" + string(make([]byte, 100))))

	tests := []struct {
		name         string
		declaredMime string
		base64Data   string
		expected     string
	}{
		{"correct png declaration", "image/png", pngHeader, "image/png"},
		{"correct jpeg declaration", "image/jpeg", jpegHeader, "image/jpeg"},
		{"correct webp declaration", "image/webp", webpHeader, "image/webp"},
		{"correct gif declaration", "image/gif", gifHeader, "image/gif"},

		{"png declared but actually webp", "image/png", webpHeader, "image/webp"},
		{"png declared but actually jpeg", "image/png", jpegHeader, "image/jpeg"},
		{"jpeg declared but actually png", "image/jpeg", pngHeader, "image/png"},
		{"jpeg declared but actually webp", "image/jpeg", webpHeader, "image/webp"},

		{"empty data returns declared", "image/png", "", "image/png"},
		{"invalid base64 returns declared", "image/png", "!!!not-base64!!!", "image/png"},

		{"case-insensitive match no false positive", "image/PNG", pngHeader, "image/PNG"},
		{"mime with params no false positive", "image/png; charset=binary", pngHeader, "image/png; charset=binary"},

		{"undetectable format returns declared", "image/heic", base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x1Cftyp" + string(make([]byte, 100)))), "image/heic"},

		{"non-image detected returns declared", "image/svg+xml",
			base64.StdEncoding.EncodeToString([]byte("<?xml version=\"1.0\"?><svg></svg>" + string(make([]byte, 100)))),
			"image/svg+xml"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := DetectImageMimeType(tt.declaredMime, tt.base64Data)
			if result != tt.expected {
				t.Errorf("DetectImageMimeType(%q, ...) = %q, expected %q", tt.declaredMime, result, tt.expected)
			}
		})
	}
}
