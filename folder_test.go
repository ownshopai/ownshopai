package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dirMarker = "OwnerNameZq7x"

// markedApp is startOverApp with its data folder moved under a folder named after a fake user.
func markedApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	app, _ := startOverApp(t)
	dir := filepath.Join(t.TempDir(), "Users", dirMarker, "ownshop")
	if err := os.CopyFS(dir, os.DirFS(app.Dir)); err != nil {
		t.Fatal(err)
	}
	app.Dir = dir
	app.File = NewShopFile(app.S, filepath.Join(dir, "products.xlsx"))
	app.File.Load()
	return app, app.Routes()
}

func TestOwnerPagesNeverShowDataDir(t *testing.T) {
	app, h := markedApp(t)
	pages := []string{"/setup/1", "/setup/2", "/setup/3", "/setup/4", "/setup/type", "/settings", "/products",
		"/orders", "/calendar", "/chats", "/faq", "/users", "/start-over/clear", "/start-over/reset",
		"/orders?msg=" + url.QueryEscape("could not open "+filepath.Join(app.Dir, "orders.csv"))}
	for _, p := range pages {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code >= 500 {
			t.Fatalf("%s: %d %s", p, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), dirMarker) || strings.Contains(rr.Header().Get("Location"), dirMarker) {
			t.Errorf("%s shows the data folder path", p)
		}
	}
	for _, p := range []string{"/products", "/orders", "/start-over/reset"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if !strings.Contains(rr.Body.String(), "your OwnShop folder") || !strings.Contains(rr.Body.String(), "Open folder") {
			t.Errorf("%s: no friendly folder text or Open folder button", p)
		}
	}
	// a POST that redirects with the backup folder in the message
	rr := soPost(h, "/start-over/clear", url.Values{"confirm": {"CLEAR"}})
	if rr.Code != http.StatusSeeOther || strings.Contains(rr.Header().Get("Location"), dirMarker) {
		t.Fatalf("clear redirect leaks the path: %d %s", rr.Code, rr.Header().Get("Location"))
	}
}

func TestOpenFolderEndpoint(t *testing.T) {
	app, h := markedApp(t)
	var opened []string
	old := openInFileManager
	openInFileManager = func(d string) error { opened = append(opened, d); return nil }
	defer func() { openInFileManager = old }()

	post := func(path, which, origin, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader("which="+which))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Requested-With", "fetch")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := post("/folder/open", "images", "http://127.0.0.1:2008", "127.0.0.1:2008"); rr.Code != http.StatusNoContent || len(opened) != 1 || opened[0] != ImagesDir(app.Dir) {
		t.Fatalf("open images: %d %v", rr.Code, opened)
	}
	if rr := post("/folder/open", "data", "", "localhost:2008"); rr.Code != http.StatusNoContent || opened[1] != app.Dir {
		t.Fatalf("open data: %d %v", rr.Code, opened)
	}
	for _, c := range []struct{ which, origin, host string }{
		{"data", "https://evil.example", "127.0.0.1:2008"},
		{"data", "null", "127.0.0.1:2008"},
		{"data", "", "shop.ownshopai.store"},
		{"../..", "", "127.0.0.1:2008"},
	} {
		if rr := post("/folder/open", c.which, c.origin, c.host); rr.Code < 400 {
			t.Errorf("%+v allowed: %d", c, rr.Code)
		}
		if rr := post("/folder/path", c.which, c.origin, c.host); rr.Code < 400 {
			t.Errorf("path %+v allowed: %d", c, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/folder/open?which=data", nil))
	if rr.Code != http.StatusMethodNotAllowed || len(opened) != 2 {
		t.Fatalf("GET must not open: %d %v", rr.Code, opened)
	}
	rr = post("/folder/path", "backups", "", "127.0.0.1:2008")
	var j map[string]string
	json.Unmarshal(rr.Body.Bytes(), &j)
	if rr.Code != 200 || j["path"] != filepath.Join(app.Dir, "backups") {
		t.Fatalf("path: %d %v", rr.Code, j)
	}
	// the public chat listener does not serve it
	app.Pub = newPubSite(app, func() string { return "shop.ownshopai.store" })
	for _, p := range []string{"/folder/open", "/folder/path"} {
		req := httptest.NewRequest("POST", p, strings.NewReader("which=data"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:2008"
		rr := httptest.NewRecorder()
		app.Pub.Handler().ServeHTTP(rr, req)
		if rr.Code < 400 || strings.Contains(rr.Body.String(), dirMarker) {
			t.Errorf("public listener served %s: %d", p, rr.Code)
		}
	}
	if len(opened) != 2 {
		t.Fatalf("opened from the public listener: %v", opened)
	}
}

// Open folder must show a window: the command may not carry hideConsole's HideWindow.
func TestFileManagerCmdIsVisible(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		c := fileManagerCmd(goos, "dir")
		if c.SysProcAttr != nil {
			t.Fatalf("%s: file manager started with SysProcAttr %+v (hidden window)", goos, c.SysProcAttr)
		}
		if c.Args[len(c.Args)-1] != "dir" {
			t.Fatalf("%s: args %v", goos, c.Args)
		}
	}
}
