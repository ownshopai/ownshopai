package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductPhotos(t *testing.T) {
	s, dir := setup(t)
	// Sample photos exist; an owner photo with odd case wins over nothing.
	if p := ProductImage(ImagesDir(dir), "hon-500"); !strings.HasSuffix(p, "HON-500.png") {
		t.Fatalf("photo lookup: %q", p)
	}
	if ProductImage(ImagesDir(dir), "NOPE") != "" {
		t.Fatal("found a photo for an unknown product")
	}
	m := fakeModel{
		"do you have honey": "call:search_product{query:<escape>honey<escape>}",
		"add 2 raw honey":   "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}",
		"slot 1":            "call:pick_slot{slot:<escape>1<escape>}",
		"yes":               "call:confirm_order{answer:<escape>yes<escape>}",
	}
	cap := &capture{name: "fake", account: "shop1"}
	hub := NewHub(nil)
	hub.Add(cap)
	b := &Bot{S: s, M: m, Send: hub, Dir: dir}
	say := func(text string) []Out {
		return b.Handle(context.Background(), Inbound{Channel: "fake", Account: "shop1", SenderID: "chat-7", Phone: "+31600000007", Name: "Photo Buyer", Text: text})
	}
	if o := say("do you have honey"); len(o) == 0 || !strings.Contains(o[0].Image, "HON-") {
		t.Fatalf("search has no photo: %+v", o)
	}
	if o := say("add 2 raw honey"); len(o) == 0 || !strings.HasSuffix(o[0].Image, "HON-500.png") {
		t.Fatalf("cart has no photo: %+v", o)
	}
	say("slot 1")
	if o := say("yes"); len(o) != 1 || o[0].kind != "ask_address" {
		t.Fatalf("no address question: %+v", o)
	}
	// A long address must still fit on the invoice image (wrapped, then cut).
	o := say("Flat 12B, Long Building Name, 4 Orchard Road, Near The Old Market, Leeds LS1 4AB, United Kingdom " + strings.Repeat("x", 200))[1:]
	if len(o) == 0 || !strings.Contains(o[0].Image, filepath.Join("invoices", "invoice-")) {
		t.Fatalf("invoice is not an image: %+v", o)
	}
	if st, err := os.Stat(o[0].Image); err != nil || st.Size() < 2000 {
		t.Fatalf("invoice png: %v", err)
	}
	if out := os.Getenv("INVOICE_OUT"); out != "" {
		data, _ := os.ReadFile(o[0].Image)
		os.WriteFile(out, data, 0o644)
	}
}

func TestSaveProductImageRejects(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProductImage(dir, "../x", "a.png", strings.NewReader("x")); err == nil {
		t.Fatal("path escape accepted")
	}
	if err := SaveProductImage(dir, "HON-500", "a.exe", strings.NewReader("x")); err == nil {
		t.Fatal("non-photo extension accepted")
	}
	if err := SaveProductImage(dir, "HON-500", "a.png", strings.NewReader("not a png")); err == nil {
		t.Fatal("non-photo content accepted")
	}
}
