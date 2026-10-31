package main

import (
	"fmt"
	"os"

	"github.com/sidenor/toget/internal/qr"
)

// writeSecretQR renders a bearer secret as a terminal QR code and/or a PNG.
// Invite and pairing codes grant one-time access, so a written PNG is always
// mode 0600 even if the destination file already existed.
func writeSecretQR(payload, pngPath string, terminal bool) error {
	if terminal {
		text, err := qr.Text(payload)
		if err != nil {
			return err
		}
		fmt.Print("\n" + text)
	}
	if pngPath != "" {
		data, err := qr.PNG(payload, 10)
		if err != nil {
			return err
		}
		if err := os.WriteFile(pngPath, data, 0o600); err != nil {
			return err
		}
		if err := os.Chmod(pngPath, 0o600); err != nil {
			return err
		}
		fmt.Printf("\nWrote QR code to %s (mode 0600)\n", pngPath)
	}
	return nil
}
