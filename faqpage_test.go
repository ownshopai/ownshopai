package main

import (
	"bytes"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestFAQPageAddEditDelete(t *testing.T) {
	s, srv := webShop(t)
	var dir string
	s.DB.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &dir)
	xlsx := filepath.Join(filepath.Dir(dir), "products.xlsx")
	sheet := func() string {
		f, err := excelize.OpenFile(xlsx)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		rows, _ := f.GetRows(sheetFAQ)
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(strings.Join(r, "|") + "\n")
		}
		return b.String()
	}
	say := func(text string) string {
		var out bytes.Buffer
		if err := sayCmd([]string{"--user", "ana", "--password", "secret", "--url", srv.URL, "--quiet", "300ms", "--timeout", "10s", text}, &out); err != nil {
			t.Fatalf("say %q: %v", text, err)
		}
		return out.String()
	}
	before, _ := s.FAQs()
	res, err := http.PostForm(srv.URL+"/faq", url.Values{"question": {"Do you gift wrap?"},
		"answer": {"Yes, gift wrap is free on any order. Just ask when you order."}, "keywords": {"gift, wrap, present"}})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("add: %v %v", err, res)
	}
	if !strings.Contains(sheet(), "Do you gift wrap?|Yes, gift wrap is free on any order. Just ask when you order.|gift, wrap, present") {
		t.Fatalf("xlsx FAQ sheet:\n%s", sheet())
	}
	fs, _ := s.FAQs()
	if len(fs) != len(before)+1 {
		t.Fatalf("faq rows %d -> %d", len(before), len(fs))
	}
	if got := say("can you gift wrap it?"); !strings.Contains(got, "gift wrap is free") {
		t.Fatalf("bot did not answer the new FAQ: %q", got)
	}
	page := readAll(mustGet(t, srv.URL+"/faq"))
	if !strings.Contains(page, "Do you gift wrap?") {
		t.Fatal("FAQ page does not list it")
	}
	id := strconvI(fs[len(fs)-1].ID)
	http.PostForm(srv.URL+"/faq/"+id, url.Values{"question": {"Do you gift wrap?"}, "answer": {"Gift wrap costs 50."}, "keywords": {"gift, wrap"}})
	if !strings.Contains(sheet(), "Gift wrap costs 50.") || strings.Contains(sheet(), "is free on any order") {
		t.Fatalf("edit not in xlsx:\n%s", sheet())
	}
	if got := say("can you gift wrap it?"); !strings.Contains(got, "Gift wrap costs 50.") {
		t.Fatalf("bot did not use the edit: %q", got)
	}
	fs, _ = s.FAQs()
	http.PostForm(srv.URL+"/faq/"+strconvI(fs[len(fs)-1].ID)+"/delete", nil)
	if strings.Contains(sheet(), "gift") {
		t.Fatalf("delete not in xlsx:\n%s", sheet())
	}
	if fs, _ := s.FAQs(); len(fs) != len(before) {
		t.Fatalf("after delete %d, want %d", len(fs), len(before))
	}
	if res, _ := http.PostForm(srv.URL+"/faq", url.Values{"question": {"x"}}); !strings.Contains(res.Request.URL.RawQuery, "Not+saved") {
		t.Fatalf("empty answer accepted: %s", res.Request.URL)
	}
}

func mustGet(t *testing.T, u string) *http.Response {
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
