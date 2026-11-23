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
	"golang.org/x/image/math/fixed"
)

// writeSamplePhotos makes a simple labelled tile per sample product (images/<sku>.png), so the demo
// shop shows photos out of the box. A real shop replaces them with real photos.
func writeSamplePhotos(dir string, rows [][]any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tints := map[string]color.RGBA{
		"HON": {0xf3, 0xc6, 0x5f, 0xff}, "OIL": {0xc9, 0xd8, 0x8f, 0xff}, "FLR": {0xe8, 0xdc, 0xc4, 0xff},
		"TEA": {0x9f, 0xc9, 0x9a, 0xff}, "SOP": {0xd9, 0xc8, 0xe8, 0xff}, "NUT": {0xd2, 0xa6, 0x79, 0xff},
		"GRN": {0xe6, 0xd3, 0xa3, 0xff},
	}
	big, small := face(gobold.TTF, 40), face(goregular.TTF, 22)
	for _, r := range rows {
		sku, name := fmt.Sprint(r[0]), fmt.Sprint(r[1])
		if ProductImage(dir, sku) != "" {
			continue // keep the owner's own photo
		}
		tint, ok := tints[strings.SplitN(sku, "-", 2)[0]]
		if !ok {
			tint = color.RGBA{0xdd, 0xdd, 0xdd, 0xff}
		}
		const S = 480
		img := image.NewRGBA(image.Rect(0, 0, S, S))
		draw.Draw(img, img.Bounds(), image.NewUniform(tint), image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(40, 300, S-40, S-40), image.NewUniform(color.NRGBA{0xff, 0xff, 0xff, 0xd0}), image.Point{}, draw.Over)
		ink := image.NewUniform(color.RGBA{0x22, 0x22, 0x22, 0xff})
		words := strings.Fields(name)
		l1, l2 := "", ""
		for _, w := range words {
			if len(l1)+len(w) < 20 && l2 == "" {
				l1 = strings.TrimSpace(l1 + " " + w)
			} else {
				l2 = strings.TrimSpace(l2 + " " + w)
			}
		}
		put := func(f font.Face, y int, s string) {
			d := &font.Drawer{Dst: img, Src: ink, Face: f}
			d.Dot = fixed.P((S-d.MeasureString(s).Round())/2, y)
			d.DrawString(s)
		}
		put(big, 180, strings.ToUpper(string([]rune(words[0])[0:1])))
		put(small, 360, l1)
		put(small, 395, l2)
		put(small, 425, sku)
		f, err := os.Create(filepath.Join(dir, sku+".png"))
		if err != nil {
			return err
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
