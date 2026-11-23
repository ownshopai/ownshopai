package main

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Product photos live next to products.xlsx in an "images" folder, named by the product's ID
// (the SKU column): images/HONEY-01.jpg. Any of .jpg .jpeg .png .webp works, in any letter case.
// The owner can drop files there or upload them on the Products page. One photo per product for now;
// later photos (HONEY-01-B.jpg, ...) are ignored.

var imageExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// ImagesDir is the photo folder for a data folder.
func ImagesDir(dataDir string) string { return filepath.Join(dataDir, "images") }

// ProductImage returns the photo file for a product ID, or "" when there is none.
func ProductImage(dir, sku string) string {
	sku = strings.TrimSpace(sku)
	if dir == "" || sku == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), sku) {
			continue
		}
		for _, x := range imageExts {
			if ext == x {
				return filepath.Join(dir, name)
			}
		}
	}
	return ""
}

var skuSafe = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,64}$`)

// SaveProductImage stores an uploaded photo as images/<sku>.<ext>, replacing any older photo for it.
func SaveProductImage(dir, sku, filename string, src io.Reader) error {
	sku = strings.TrimSpace(sku)
	if !skuSafe.MatchString(sku) || strings.Contains(sku, "..") {
		return fmt.Errorf("product ID %q cannot be used as a file name", sku)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	ok := false
	for _, x := range imageExts {
		ok = ok || ext == x
	}
	if !ok {
		return fmt.Errorf("use a .jpg, .png or .webp photo")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(src, 15<<20))
	if err != nil {
		return err
	}
	if _, _, err := image.DecodeConfig(strings.NewReader(string(data))); err != nil {
		return fmt.Errorf("that file is not a photo we can read")
	}
	for old := ProductImage(dir, sku); old != ""; old = ProductImage(dir, sku) {
		if err := os.Remove(old); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, sku+ext), data, 0o644)
}

// thumb loads a photo and scales it to fit a size x size square (keeping its shape), or nil.
func thumb(path string, size int) image.Image {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	b := src.Bounds()
	w, h := size, size
	if b.Dx() > b.Dy() {
		h = size * b.Dy() / b.Dx()
	} else {
		w = size * b.Dx() / b.Dy()
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// serveProductImage serves images/<sku>.* for the Products page.
func (a *App) serveProductImage(w http.ResponseWriter, r *http.Request) {
	p := ProductImage(ImagesDir(a.Dir), r.PathValue("sku"))
	if p == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

// uploadProductImage takes one photo for a product from the Products page.
func (a *App) uploadProductImage(w http.ResponseWriter, r *http.Request) {
	sku := r.PathValue("sku")
	f, hdr, err := r.FormFile("photo")
	if err != nil {
		back(w, r, "/products", "Choose a photo first.")
		return
	}
	defer f.Close()
	if err := SaveProductImage(ImagesDir(a.Dir), sku, hdr.Filename, f); err != nil {
		back(w, r, "/products", "Could not save the photo: "+err.Error())
		return
	}
	back(w, r, "/products", "Photo saved for "+sku+".")
}
