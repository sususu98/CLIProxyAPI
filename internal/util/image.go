package util

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"strings"
)

// DetectImageMimeType detects the actual image MIME type from base64 data and
// returns it if it differs from the declared type. This corrects mismatches
// where clients declare e.g. image/png but actually send image/webp.
func DetectImageMimeType(declaredMime, base64Data string) string {
	if base64Data == "" {
		return declaredMime
	}

	// 512 raw bytes needed → ceil(512/3)*4 = 684 base64 chars
	sample := base64Data
	if len(sample) > 684 {
		sample = sample[:684]
	}

	sample = strings.TrimRight(sample, "=\n\r ")

	raw, err := base64.StdEncoding.DecodeString(sample + strings.Repeat("=", (4-len(sample)%4)%4))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(sample)
		if err != nil {
			return declaredMime
		}
	}

	detected := http.DetectContentType(raw)
	if strings.HasPrefix(detected, "image/") {
		normalizedDeclared := strings.ToLower(strings.TrimSpace(declaredMime))
		if i := strings.IndexByte(normalizedDeclared, ';'); i >= 0 {
			normalizedDeclared = strings.TrimSpace(normalizedDeclared[:i])
		}
		if detected != normalizedDeclared {
			return detected
		}
	}

	return declaredMime
}

func CreateWhiteImageBase64(aspectRatio string) (string, error) {
	width := 1024
	height := 1024

	switch aspectRatio {
	case "1:1":
		width = 1024
		height = 1024
	case "2:3":
		width = 832
		height = 1248
	case "3:2":
		width = 1248
		height = 832
	case "3:4":
		width = 864
		height = 1184
	case "4:3":
		width = 1184
		height = 864
	case "4:5":
		width = 896
		height = 1152
	case "5:4":
		width = 1152
		height = 896
	case "9:16":
		width = 768
		height = 1344
	case "16:9":
		width = 1344
		height = 768
	case "21:9":
		width = 1536
		height = 672
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)

	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}

	base64String := base64.StdEncoding.EncodeToString(buf.Bytes())
	return base64String, nil
}
