// Package qr renders arbitrary Toget payloads as QR codes for the terminal
// and as PNG images. Payload semantics and trust belong to the caller.
package qr

import (
	"errors"
	"strings"

	rscqr "rsc.io/qr"
)

const (
	defaultScale = 8
	maxScale     = 32
)

var (
	// ErrEmpty means there is nothing to encode.
	ErrEmpty = errors.New("QR payload is empty")
	// ErrTooLarge means the payload exceeds what a QR code can carry.
	ErrTooLarge = errors.New("payload is too large for a QR code")
	// ErrScale means the requested PNG scale is out of range.
	ErrScale = errors.New("QR scale must be between 2 and 32")
)

// PNG renders payload as a PNG QR code. scale is pixels per module. It
// prefers medium error correction and falls back to low correction for
// unusually large payloads.
func PNG(payload string, scale int) ([]byte, error) {
	if scale == 0 {
		scale = defaultScale
	}
	if scale < 2 || scale > maxScale {
		return nil, ErrScale
	}
	code, err := encode(payload)
	if err != nil {
		return nil, err
	}
	code.Scale = scale
	return code.PNG(), nil
}

// Text renders payload as a QR code using half-block characters. The result
// has a two-module quiet zone and is suitable for an ordinary UTF-8 terminal.
func Text(payload string) (string, error) {
	code, err := encode(payload)
	if err != nil {
		return "", err
	}
	const quiet = 2
	size := code.Size + quiet*2
	var out strings.Builder
	out.Grow((size + 1) * (size/2 + 1) * 3)
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			top := black(code, x-quiet, y-quiet)
			bottom := y+1 < size && black(code, x-quiet, y+1-quiet)
			switch {
			case top && bottom:
				out.WriteRune('█')
			case top:
				out.WriteRune('▀')
			case bottom:
				out.WriteRune('▄')
			default:
				out.WriteByte(' ')
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}

func black(code *rscqr.Code, x, y int) bool {
	if code == nil || x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}

func encode(payload string) (*rscqr.Code, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, ErrEmpty
	}
	code, err := rscqr.Encode(payload, rscqr.M)
	if err == nil {
		return code, nil
	}
	code, err = rscqr.Encode(payload, rscqr.L)
	if err != nil {
		return nil, ErrTooLarge
	}
	return code, nil
}
