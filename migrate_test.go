package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacy(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "washop")
	for p, body := range map[string]string{
		"products.xlsx":          "xlsx",
		"images/HON-500.png":     "png",
		"wacli/shop1/session.db": "wa-session",
		"uploads/payment-qr.png": "qr",
		"bin/wacli":              "helper",
		"washop.log":             "log",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(old, p)), 0o755)
		os.WriteFile(filepath.Join(old, p), []byte(body), 0o644)
	}
	st, err := OpenStore(filepath.Join(old, "washop.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Set("shop_name", "Green Leaf Organics")
	st.Set("qr_path", filepath.Join(old, "uploads", "payment-qr.png"))
	st.DB.Exec(`PRAGMA foreign_keys = OFF`)
	if _, err := st.DB.Exec(`INSERT INTO receipts(order_id, path, created) VALUES (1, ?, 'now')`, filepath.Join(old, "receipts", "receipt-1.png")); err != nil {
		t.Fatal(err)
	}
	st.DB.Close()

	moved, err := migrateLegacy(base)
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	nd := filepath.Join(base, "ownshop")
	for p, want := range map[string]string{"products.xlsx": "xlsx", "images/HON-500.png": "png", "wacli/shop1/session.db": "wa-session", "ownshop.log": "log"} {
		if b, err := os.ReadFile(filepath.Join(nd, p)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(nd, "bin")); err == nil {
		t.Error("bin/ should not be copied")
	}
	st, err = OpenStore(filepath.Join(nd, "ownshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Get("shop_name") != "Green Leaf Organics" {
		t.Error("shop name not migrated")
	}
	if got, want := st.Get("qr_path"), filepath.Join(nd, "uploads", "payment-qr.png"); got != want {
		t.Errorf("qr_path %q, want %q", got, want)
	}
	var rp string
	st.DB.QueryRow(`SELECT path FROM receipts WHERE order_id = 1`).Scan(&rp)
	if want := filepath.Join(nd, "receipts", "receipt-1.png"); rp != want {
		t.Errorf("receipt path %q, want %q", rp, want)
	}
	st.DB.Close()
	// the old folder is a backup: untouched
	if _, err := os.Stat(filepath.Join(old, "washop.db")); err != nil {
		t.Error("old database gone")
	}
	if _, err := os.Stat(filepath.Join(base, "ownshop.migrating")); err == nil {
		t.Error("temporary folder left behind")
	}
	// second start: the new folder exists, nothing happens
	os.WriteFile(filepath.Join(old, "products.xlsx"), []byte("changed"), 0o644)
	if moved, _ := migrateLegacy(base); moved {
		t.Error("migrated twice")
	}
	if b, _ := os.ReadFile(filepath.Join(nd, "products.xlsx")); string(b) != "xlsx" {
		t.Error("new folder overwritten")
	}
}

func TestMigrateLegacyNothingOld(t *testing.T) {
	base := t.TempDir()
	if moved, err := migrateLegacy(base); moved || err != nil {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
}

func TestRetireLegacyStartup(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	la := filepath.Join(home, "Library", "LaunchAgents", "com.deemwar.washop.plist")
	os.MkdirAll(filepath.Dir(la), 0o755)
	os.WriteFile(la, []byte("plist"), 0o644)
	m := retireLegacyStartup(home, "darwin", data)
	if len(m) != 1 {
		t.Fatalf("moved %v", m)
	}
	if _, err := os.Stat(la); err == nil {
		t.Error("old login item still active")
	}
	if b, _ := os.ReadFile(filepath.Join(data, "retired-shortcuts", "com.deemwar.washop.plist")); string(b) != "plist" {
		t.Error("old login item not kept")
	}
}

// A command run before the first start can unpack a helper into <new>/bin; that must not block the move.
func TestMigrateLegacyIntoBareFolder(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "washop"), 0o755)
	st, _ := OpenStore(filepath.Join(base, "washop", "washop.db"))
	st.Set("shop_name", "Green Leaf Organics")
	st.DB.Close()
	os.MkdirAll(filepath.Join(base, "ownshop", "bin"), 0o755)
	if moved, err := migrateLegacy(base); !moved || err != nil {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	st, err := OpenStore(filepath.Join(base, "ownshop", "ownshop.db"))
	if err != nil || st.Get("shop_name") != "Green Leaf Organics" {
		t.Fatalf("shop not moved: %v", err)
	}
	st.DB.Close()
}

// A failed copy leaves no new shop behind, and the next start tries again.
func TestMigrateLegacyFailureRetries(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	base := t.TempDir()
	old := filepath.Join(base, "washop")
	os.MkdirAll(old, 0o755)
	st, _ := OpenStore(filepath.Join(old, "washop.db"))
	st.DB.Close()
	locked := filepath.Join(old, "orders.csv")
	os.WriteFile(locked, []byte("x"), 0o000)
	if moved, err := migrateLegacy(base); moved || err == nil {
		t.Fatalf("want a failure, got moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(base, "ownshop")); err == nil {
		t.Fatal("a failed copy left a new folder")
	}
	os.Chmod(locked, 0o644)
	if moved, err := migrateLegacy(base); !moved || err != nil {
		t.Fatalf("retry: moved=%v err=%v", moved, err)
	}
}
