package card

import (
	"errors"
	"strings"

	"rsc.io/qr"
)

const (
	defaultQRScale = 8
	maxQRScale     = 32
)

// QRPNG renders the portable text as a PNG QR code. It prefers medium error
// correction and falls back to low correction if the card is unusually large.
func QRPNG(payload string, scale int) ([]byte, error) {
	if scale == 0 {
		scale = defaultQRScale
	}
	if scale < 2 || scale > maxQRScale {
		return nil, errors.New("QR scale must be between 2 and 32")
	}
	code, err := encodeQR(payload)
	if err != nil {
		return nil, err
	}
	code.Scale = scale
	return code.PNG(), nil
}

// QRText renders a QR code with half-block characters for a terminal.
func QRText(payload string) (string, error) {
	code, err := encodeQR(payload)
	if err != nil {
		return "", err
	}
	const quiet = 2
	size := code.Size + quiet*2
	var out strings.Builder
	out.Grow((size + 1) * (size/2 + 1) * 3)
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			top := qrBlack(code, x-quiet, y-quiet)
			bottom := y+1 < size && qrBlack(code, x-quiet, y+1-quiet)
			switch {
			case top && bottom:
				out.WriteRune('█')
			case top:
				out.WriteRune('▀')
			case bottom:
				out.WriteRune('▄')
			default:
				out.WriteRune(' ')
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}

func qrBlack(code *qr.Code, x, y int) bool {
	if code == nil || x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}

func encodeQR(payload string) (*qr.Code, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, errors.New("QR payload is empty")
	}
	code, err := qr.Encode(payload, qr.M)
	if err == nil {
		return code, nil
	}
	code, err = qr.Encode(payload, qr.L)
	if err != nil {
		return nil, errors.New("card is too large for a QR code")
	}
	return code, nil
}
