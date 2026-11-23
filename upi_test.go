package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestUPILinkEncoding(t *testing.T) {
	eq(t, "link", UPILink("shop@okhdfc", "Hill & Dale Store", 26.8, 1042),
		"upi://pay?pa=shop@okhdfc&pn=Hill%20%26%20Dale%20Store&am=26.80&cu=INR&tn=Order%201042")
}

func TestUPIQRIsPNG(t *testing.T) {
	path, err := WriteUPIQR(UPILink("a@b", "X", 1, 1), t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	if img, err := png.Decode(f); err != nil || img.Bounds().Dx() < 100 {
		t.Fatalf("not a QR image: %v", err)
	}
}

func TestPayMessageCombinations(t *testing.T) {
	h := newHarness(t)
	h.b.Dir = h.dir
	h.buy()
	h.say(call("confirm_order", ""), "yes")
	ask := func() string { return h.say(call("send_payment_qr", ""), "how do I pay") }
	qr := h.s.Get("qr_path")
	upi := "upi://pay?pa=shop@okhdfc&pn=Test%20Shop&am=26.80&cu=INR&tn=Order%201042"
	img := " [image " + filepath.Join(h.dir, "invoices", "upi-1042.png") + "]"

	h.s.Set("qr_path", "")
	eq(t, "nothing", ask(), "Test Shop: please pay INR 26.80. Put order 1042 in the payment note or reference. The shop will send payment details shortly.")
	h.s.Set("pay_note", "Bank: X, Account: Y, IBAN: Z.")
	eq(t, "bank only", ask(), "Test Shop: please pay INR 26.80. Bank transfer: Bank: X, Account: Y, IBAN: Z. Put order 1042 in the payment note or reference.")
	h.s.Set("pay_link", "https://pay.example.test/x")
	eq(t, "link+bank", ask(), "Test Shop: please pay INR 26.80 at https://pay.example.test/x. Bank transfer: Bank: X, Account: Y, IBAN: Z. Put order 1042 in the payment note or reference.")
	h.s.Set("upi_id", "shop@okhdfc")
	eq(t, "upi+link+bank", ask(), "Test Shop: please pay INR 26.80 by UPI using this QR code.\nScan with any UPI app; amount and order number are filled in: "+upi+
		"\nOr pay at https://pay.example.test/x\nBank transfer: Bank: X, Account: Y, IBAN: Z\nPut order 1042 in the payment note or reference."+img)
	h.s.Set("pay_link", "")
	h.s.Set("pay_note", "")
	h.s.Set("qr_path", qr)
	eq(t, "upi+qr", ask(), "Test Shop: please pay INR 26.80 by UPI using this QR code.\nScan with any UPI app; amount and order number are filled in: "+upi+
		"\nWe also take our other payment QR; ask us for it.\nPut order 1042 in the payment note or reference."+img)
	h.s.Set("upi_id", "")
	eq(t, "qr only", ask(), "Test Shop: please pay INR 26.80 using this QR code. Put order 1042 in the payment note or reference. [image "+qr+"]")
}
