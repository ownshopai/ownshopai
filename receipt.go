package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func face(ttf []byte, size float64) font.Face {
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic(err)
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	return fc
}

// RenderReceipt draws a plain receipt PNG for a paid order.
func RenderReceipt(s *Store, o *Order) image.Image { return renderDoc(s, o, "", false) }

// skuByID maps product row IDs to the owner's product IDs (SKU), for finding photos.
func skuByID(s *Store) map[int64]string {
	m := map[int64]string{}
	ps, _ := s.Products()
	for _, p := range ps {
		m[p.ID] = p.SKU
	}
	return m
}

// renderDoc draws the invoice (invoice=true, sent at confirmation) or the receipt (after payment)
// as a PNG. Each line shows the product photo from imgDir when there is one.
func renderDoc(s *Store, o *Order, imgDir string, invoice bool) image.Image {
	const W = 760
	rowH := 34
	var thumbs []image.Image
	if imgDir != "" {
		skus := skuByID(s)
		for _, it := range o.Items {
			t := thumb(ProductImage(imgDir, skus[it.ProductID]), 64)
			thumbs = append(thumbs, t)
			if t != nil {
				rowH = 76
			}
		}
	}
	deliver := wrapText("Deliver to: "+o.DeliverTo, 70, 3)
	if o.DeliverTo == "" {
		deliver = nil
	}
	h := 330 + rowH*len(o.Items) + 24*len(deliver)
	img := image.NewRGBA(image.Rect(0, 0, W, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	ink := image.NewUniform(color.RGBA{0x22, 0x22, 0x22, 0xff})
	grey := image.NewUniform(color.RGBA{0x77, 0x77, 0x77, 0xff})
	title, bold, reg := face(gobold.TTF, 30), face(gobold.TTF, 18), face(goregular.TTF, 18)
	text := func(f font.Face, src image.Image, x, y int, str string, right bool) {
		d := &font.Drawer{Dst: img, Src: src, Face: f}
		if right {
			x -= d.MeasureString(str).Round()
		}
		d.Dot = fixed.P(x, y)
		d.DrawString(str)
	}
	line := func(y int) {
		draw.Draw(img, image.Rect(40, y, W-40, y+1), grey, image.Point{}, draw.Src)
	}
	cur := s.Get("currency")
	money := func(v float64) string { return fmt.Sprintf("%s %.2f", cur, v) }
	y := 60
	text(title, ink, 40, y, s.Get("shop_name"), false)
	if a := s.Get("address"); a != "" {
		y += 28
		text(reg, grey, 40, y, a, false)
	}
	y += 40
	label, when := "RECEIPT", o.PaidAt
	if invoice {
		label = "INVOICE"
	} else if when == "" {
		when = now()
	}
	text(bold, ink, 40, y, label, false)
	text(reg, ink, W-40, y, fmt.Sprintf("Order %d", o.ID), true)
	y += 26
	if invoice {
		text(reg, grey, 40, y, "To pay "+money(o.Total)+", order "+fmt.Sprint(o.ID)+" in the payment note", false)
	} else {
		text(reg, grey, 40, y, "Paid "+when, false)
	}
	text(reg, grey, W-40, y, o.Invoice, true)
	for _, l := range deliver {
		y += 24
		text(reg, grey, 40, y, l, false)
	}
	y += 20
	line(y)
	y += 30
	text(bold, ink, 40, y, "Item", false)
	text(bold, ink, 470, y, "Qty", true)
	text(bold, ink, 590, y, "Unit", true)
	text(bold, ink, W-40, y, "Amount", true)
	for i, it := range o.Items {
		y += rowH
		name := it.Name
		if len(name) > 34 {
			name = name[:33] + "."
		}
		x := 40
		if rowH > 34 {
			x = 120
			if i < len(thumbs) && thumbs[i] != nil {
				t := thumbs[i]
				tb := t.Bounds()
				top := y - 46 - tb.Dy()/2 + 8
				draw.Draw(img, image.Rect(40+(64-tb.Dx())/2, top, 40+(64-tb.Dx())/2+tb.Dx(), top+tb.Dy()), t, tb.Min, draw.Over)
			}
			if len(name) > 26 {
				name = name[:25] + "."
			}
		}
		text(reg, ink, x, y-24*boolInt(rowH > 34), name, false)
		y -= 24 * boolInt(rowH > 34)
		text(reg, ink, 470, y, fmt.Sprint(it.Qty), true)
		text(reg, ink, 590, y, fmt.Sprintf("%.2f", it.Price), true)
		text(reg, ink, W-40, y, fmt.Sprintf("%.2f", float64(it.Qty)*it.Price), true)
		y += 24 * boolInt(rowH > 34)
	}
	y += 20
	line(y)
	y += 34
	total := "Total paid"
	if invoice {
		total = "Total to pay"
	}
	text(bold, ink, 40, y, total, false)
	text(bold, ink, W-40, y, money(o.Total), true)
	y += 50
	footer := strings.TrimSpace(s.Get("receipt_footer"))
	if invoice {
		footer = "Pay with the QR or link in the next message. Receipt follows."
	} else if footer == "" {
		footer = "Thank you."
	}
	text(reg, grey, 40, y, footer, false)
	return img
}

// wrapText splits s into at most max lines of about width characters; the last line is cut with "...".
func wrapText(s string, width, max int) []string {
	var lines []string
	r := []rune(s)
	for len(r) > 0 && len(lines) < max {
		n := width
		if n >= len(r) {
			n = len(r)
		} else if i := strings.LastIndexAny(string(r[:n]), " ,/"); i > 0 {
			n = len([]rune(string(r[:n])[:i+1]))
		}
		lines = append(lines, strings.TrimSpace(string(r[:n])))
		r = r[n:]
	}
	if len(r) > 0 {
		last := []rune(lines[len(lines)-1])
		if len(last) > width-3 {
			last = last[:width-3]
		}
		lines[len(lines)-1] = string(last) + "..."
	}
	return lines
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// WriteInvoice renders the invoice with product photos into dir/invoices. Returns the file path.
func WriteInvoice(s *Store, o *Order, dir string) (string, error) {
	return writePNG(filepath.Join(dir, "invoices"), fmt.Sprintf("invoice-%d.png", o.ID), renderDoc(s, o, ImagesDir(dir), true))
}

// WriteReceipt renders the receipt into dir/receipts and records it in SQLite. Returns the file path.
func WriteReceipt(s *Store, o *Order, dir string) (string, error) {
	path, err := writePNG(filepath.Join(dir, "receipts"), fmt.Sprintf("receipt-%d.png", o.ID), renderDoc(s, o, ImagesDir(dir), false))
	if err != nil {
		return "", err
	}
	return path, s.SaveReceipt(o.ID, path)
}

func writePNG(rd, name string, img image.Image) (string, error) {
	if err := os.MkdirAll(rd, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(rd, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
