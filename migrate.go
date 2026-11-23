package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The product was called washop before it became OwnShop AI. An owner who updates keeps the shop:
// on the first start, if the old data folder (<config>/washop) exists and the new one (<config>/ownshop)
// does not, everything in it is copied over, so products, orders, photos, settings and each linked
// WhatsApp number (wacli stores under wacli/) keep working. The old folder is left untouched as a backup.

const legacyAppID = "washop"

// migrateLegacy copies base/washop to base/ownshop when the old folder exists and the new one holds no
// shop yet (no ownshop.db; a bare bin/ from a helper unpacked by a command does not count). It reports
// whether it copied. The copy goes to a temporary folder first and is moved in at the end, so a failure
// halfway never leaves a half-filled shop that would stop a later retry.
func migrateLegacy(base string) (bool, error) {
	oldDir, newDir := filepath.Join(base, legacyAppID), filepath.Join(base, appID)
	if _, err := os.Stat(filepath.Join(newDir, appID+".db")); err == nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(oldDir, legacyAppID+".db")); err != nil {
		return false, nil
	}
	tmp := newDir + ".migrating"
	os.RemoveAll(tmp)
	fail := func(err error) (bool, error) { os.RemoveAll(tmp); return false, err }
	if err := copyTree(oldDir, tmp); err != nil {
		return fail(fmt.Errorf("copy %s: %w", oldDir, err))
	}
	// The database and log carry the product name in their file names.
	for _, sfx := range []string{".db", ".db-wal", ".db-shm", ".log"} {
		p := filepath.Join(tmp, legacyAppID+sfx)
		if _, err := os.Stat(p); err == nil {
			if err := os.Rename(p, filepath.Join(tmp, appID+sfx)); err != nil {
				return fail(err)
			}
		}
	}
	// Full paths into the old folder (the uploaded payment QR, receipt and web chat images) now point
	// into the new one.
	st, err := OpenStore(filepath.Join(tmp, appID+".db"))
	if err != nil {
		return fail(err)
	}
	pre := oldDir + string(filepath.Separator)
	for _, tc := range [][2]string{{"settings", "value"}, {"receipts", "path"}, {"web_messages", "image"}} {
		st.DB.Exec(`UPDATE `+tc[0]+` SET `+tc[1]+` = ? || substr(`+tc[1]+`, ?) WHERE substr(`+tc[1]+`, 1, ?) = ?`,
			newDir+string(filepath.Separator), len(pre)+1, len(pre), pre)
	}
	st.DB.Close()
	if _, err := os.Stat(newDir); err != nil {
		if err := retry(func() error { return os.Rename(tmp, newDir) }); err != nil {
			return fail(err)
		}
		return true, nil
	}
	// The new folder exists without a shop (a helper was unpacked into bin/): move the copied entries in.
	ents, err := os.ReadDir(tmp)
	if err != nil {
		return fail(err)
	}
	// The database goes last: its presence is what marks the migration done.
	sort.SliceStable(ents, func(i, j int) bool { return ents[j].Name() == appID+".db" })
	for _, e := range ents {
		dst := filepath.Join(newDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := retry(func() error { return os.Rename(filepath.Join(tmp, e.Name()), dst) }); err != nil {
			return fail(err)
		}
	}
	os.RemoveAll(tmp)
	return true, nil
}

// retry runs f up to 5 times, half a second apart: on Windows a file a stopped program held can stay
// locked for a moment.
func retry(f func() error) (err error) {
	for i := 0; i < 5; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}

// copyTree copies regular files and folders from src to dst. bin/ is skipped: the helper programs
// are unpacked again from the new program on first use.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if info.IsDir() && rel == "bin" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			return copyOne(p, target, info.Mode().Perm())
		}
		return nil // sockets, links: nothing the shop needs
	})
}

func copyOne(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// retireLegacyStartup moves the old washop login item and desktop shortcut out of the way (into
// <data>/retired-shortcuts of the new folder), so the old program does not also start at login and fight the new one
// for the same WhatsApp number. Nothing is deleted.
func retireLegacyStartup(home, goos, dataDir string) (moved []string) {
	var olds []string
	switch goos {
	case "darwin":
		olds = []string{
			filepath.Join(home, "Library", "LaunchAgents", "com.deemwar."+legacyAppID+".plist"),
			filepath.Join(home, "Desktop", legacyAppID+".webloc"),
		}
	case "windows":
		olds = []string{
			filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", legacyAppID+".cmd"),
			filepath.Join(home, "Desktop", legacyAppID+".url"),
		}
	default:
		olds = []string{filepath.Join(home, ".config", "autostart", legacyAppID+".desktop")}
	}
	dst := filepath.Join(dataDir, "retired-shortcuts")
	for _, p := range olds {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if os.MkdirAll(dst, 0o755) != nil {
			return moved
		}
		to := filepath.Join(dst, filepath.Base(p))
		if err := os.Rename(p, to); err != nil {
			if copyOne(p, to, 0o644) != nil || os.Remove(p) != nil {
				continue
			}
		}
		moved = append(moved, p)
	}
	return moved
}
