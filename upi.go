package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// upiEscape encodes a UPI parameter: spaces as %20 (not +), '@' kept readable.
func upiEscape(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(v), "+", "%20"), "%40", "@")
}

// UPILink is the upi://pay link for one order: payee, name, amount, INR and the order as the note.
func UPILink(id, name string, amount float64, orderID int64) string {
	return fmt.Sprintf("upi://pay?pa=%s&pn=%s&am=%.2f&cu=INR&tn=%s",
		upiEscape(id), upiEscape(name), amount, upiEscape(fmt.Sprintf("Order %d", orderID)))
}

// WriteUPIQR writes the link as a QR PNG (pure Go encoder) into dir and returns its path.
func WriteUPIQR(link, dir string, orderID int64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("upi-%d.png", orderID))
	return path, qrcode.WriteFile(link, qrcode.Medium, 360, path)
}
