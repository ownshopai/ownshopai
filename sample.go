package main

import (
	"os"
	"path/filepath"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/xuri/excelize/v2"
)

// WriteSample writes the demo products.xlsx (Products, FAQ, Delivery slots) and an obviously fake payment QR. Demo values live only here and in sample/.
func WriteSample(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	products := [][]any{
		{"sku", "name", "description", "price", "stock", "keywords", "type", "minutes"},
		{"HON-500", "Raw honey 500g", "Unfiltered wildflower honey", 9.50, 20, "honey, raw, wildflower, sweet", "product", ""},
		{"HON-250", "Manuka style honey 250g", "Dark forest honey", 14.90, 8, "honey, forest, dark", "product", ""},
		{"OIL-500", "Cold-pressed coconut oil 500ml", "Virgin, cold-pressed", 7.80, 25, "coconut, oil, cold-pressed", "product", ""},
		{"OIL-OLV", "Extra virgin olive oil 750ml", "First cold press", 11.40, 15, "olive, oil", "product", ""},
		{"FLR-MIL", "Millet flour 1kg", "Stone-ground pearl millet", 4.20, 30, "millet, flour, gluten-free", "product", ""},
		{"FLR-BUC", "Buckwheat flour 1kg", "Stone-ground", 4.60, 18, "buckwheat, flour, gluten-free", "product", ""},
		{"TEA-HRB", "Herbal tea, chamomile and mint", "20 bags", 5.30, 40, "tea, herbal, chamomile, mint", "product", ""},
		{"TEA-GRN", "Green tea, loose leaf 100g", "Spring harvest", 6.90, 22, "tea, green, loose", "product", ""},
		{"SOP-BWX", "Beeswax soap bar", "Handmade, unscented", 4.50, 35, "soap, beeswax, handmade", "product", ""},
		{"SOP-LAV", "Lavender soap bar", "Handmade with lavender oil", 4.90, 28, "soap, lavender", "product", ""},
		{"NUT-ALB", "Almond butter 250g", "Roasted, no added sugar", 8.40, 16, "almond, butter, nut", "product", ""},
		{"NUT-PNB", "Peanut butter 350g", "Crunchy", 5.60, 20, "peanut, butter, nut", "product", ""},
		{"GRN-OAT", "Rolled oats 1kg", "Whole grain", 3.90, 30, "oats, oat, porridge", "product", ""},
		{"DRY-DAT", "Medjool dates 500g", "Soft and sweet", 9.90, 12, "dates, dried, fruit", "product", ""},
		{"SPC-TUR", "Turmeric powder 100g", "Ground turmeric root", 3.40, 26, "turmeric, spice", "product", ""},
	}
	faq := [][]any{
		{"question", "answer", "keywords"},
		{"What are your opening hours?", "We are open Monday to Saturday, 09:00 to 18:00. Orders by chat are welcome any time.", "hours, open, opening, time, closed"},
		{"Where do you deliver?", "We deliver within the city and nearby towns. Delivery is free above INR 500, otherwise INR 50.", "deliver, delivery, area, ship, shipping, where"},
		{"Can I return an item?", "Unopened items can be returned within 14 days. Write to us here with your order number.", "return, returns, refund, exchange"},
		{"Are your products certified organic?", "Yes. All food items are certified organic under the EU organic label; certificates are available on request.", "organic, certified, certificate, certification, bio"},
	}
	slots := [][]any{
		{"label", "capacity"},
		{"Saturday 10:00-12:00", 8},
		{"Saturday 14:00-16:00", 8},
		{"Monday 17:00-19:00", 6},
	}
	if err := writeWorkbook(filepath.Join(dir, "products.xlsx"), []sheet{{sheetProducts, products}, {sheetFAQ, faq}, {sheetSlots, slots}}); err != nil {
		return err
	}
	if err := writeSamplePhotos(ImagesDir(dir), products[1:]); err != nil {
		return err
	}
	return qrcode.WriteFile("https://pay.example.com/demo-shop", qrcode.Medium, 320, filepath.Join(dir, "payment-qr-demo.png"))
}

type sheet struct {
	name string
	rows [][]any
}

// writeWorkbook writes one .xlsx with a sheet per entry, in order.
func writeWorkbook(path string, sheets []sheet) error {
	f := excelize.NewFile()
	defer f.Close()
	for i, sh := range sheets {
		if i == 0 {
			f.SetSheetName(f.GetSheetName(0), sh.name)
		} else if _, err := f.NewSheet(sh.name); err != nil {
			return err
		}
		for r, row := range sh.rows {
			cell, _ := excelize.CoordinatesToCellName(1, r+1)
			if err := f.SetSheetRow(sh.name, cell, &row); err != nil {
				return err
			}
		}
	}
	return f.SaveAs(path)
}
