package qr_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/qr"
)

func TestPNG(t *testing.T) {
	data, err := qr.PNG("toget-invite:v1:test", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("QR output is not a PNG")
	}
	if _, err := qr.PNG("x", 1); !errors.Is(err, qr.ErrScale) {
		t.Fatalf("invalid scale = %v", err)
	}
	if _, err := qr.PNG("x", 33); !errors.Is(err, qr.ErrScale) {
		t.Fatalf("invalid scale = %v", err)
	}
}

func TestText(t *testing.T) {
	text, err := qr.Text("toget-pair:v1:test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "█") && !strings.Contains(text, "▀") {
		t.Fatalf("terminal QR has no blocks: %q", text)
	}
}

func TestRejectsEmptyAndOversizedPayloads(t *testing.T) {
	if _, err := qr.Text(" \n\t"); !errors.Is(err, qr.ErrEmpty) {
		t.Fatalf("empty payload = %v", err)
	}
	if _, err := qr.PNG(strings.Repeat("x", 4000), 2); !errors.Is(err, qr.ErrTooLarge) {
		t.Fatalf("oversized payload = %v", err)
	}
}
