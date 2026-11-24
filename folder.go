package main

import (
	"bytes"
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The owner pages never print the data folder's full path: on Windows and macOS it holds the
// computer's user name, which then shows up in screenshots and screen recordings. They say
// "your OwnShop folder" instead and offer an Open folder button (and a Copy folder path button
// that fetches the path on demand, so it is never in the page's HTML).

const ownFolder = "your OwnShop folder"

// openInFileManager opens dir in the OS file manager. A variable so tests can stub it.
var openInFileManager = func(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	hideConsole(cmd)
	return cmd.Start()
}

// folderFor maps the "which" form value to a folder inside the data folder.
func (a *App) folderFor(which string) (string, bool) {
	switch which {
	case "", "data":
		return a.Dir, true
	case "images":
		return ImagesDir(a.Dir), true
	case "backups":
		return filepath.Join(a.Dir, "backups"), true
	}
	return "", false
}

// ownerLocal lets a request through only from the owner page on this computer: a localhost Host
// and, when the browser sends one, a localhost Origin (so no other web site can post here).
func ownerLocal(w http.ResponseWriter, r *http.Request) bool {
	if !localHost(r.Host) {
		http.Error(w, "the shop page is served on this computer only", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); o == "null" || err != nil || !localHost(u.Host) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
	}
	return true
}

func (a *App) folderRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /folder/open", func(w http.ResponseWriter, r *http.Request) {
		if !ownerLocal(w, r) {
			return
		}
		dir, ok := a.folderFor(r.FormValue("which"))
		if !ok || a.Dir == "" {
			http.Error(w, "unknown folder", http.StatusBadRequest)
			return
		}
		os.MkdirAll(dir, 0o755)
		if err := openInFileManager(dir); err != nil {
			http.Error(w, "could not open the folder", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("X-Requested-With") == "fetch" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		to := r.Header.Get("Referer")
		if u, err := url.Parse(to); err != nil || to == "" || !localHost(u.Host) {
			to = "/"
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	})
	// The path as JSON, for the Copy folder path button. Not a GET, so a link or an image tag on
	// another site cannot trigger it; without CORS headers no other site can read the answer.
	m.HandleFunc("POST /folder/path", func(w http.ResponseWriter, r *http.Request) {
		if !ownerLocal(w, r) {
			return
		}
		dir, ok := a.folderFor(r.FormValue("which"))
		if !ok || a.Dir == "" {
			http.Error(w, "unknown folder", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"path": dir})
	})
}

// hideDir is the last guard: any data folder path left in owner page text (an error from the
// file system, say) is replaced by "your OwnShop folder".
func (a *App) hideDir(s string) string { return string(a.hideDirBytes([]byte(s))) }

func (a *App) hideDirBytes(b []byte) []byte {
	if len(a.Dir) < 2 {
		return b
	}
	abs, err := filepath.Abs(a.Dir) // a relative dir ("data") would also match ordinary words
	if err != nil {
		return b
	}
	var forms []string
	for _, d := range []string{abs, filepath.ToSlash(abs)} {
		forms = append(forms, d, html.EscapeString(d), template.JSEscapeString(d), url.QueryEscape(d))
	}
	for _, f := range forms {
		b = bytes.ReplaceAll(b, []byte(f), []byte(ownFolder))
	}
	return b
}

// hideDirInRedirects scrubs the data folder from redirect URLs (back() puts messages, and so any
// file system error text, in ?msg= where the address bar shows it).
func (a *App) hideDirInRedirects(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&scrubWriter{ResponseWriter: w, a: a}, r)
	})
}

type scrubWriter struct {
	http.ResponseWriter
	a    *App
	done bool
}

func (s *scrubWriter) WriteHeader(code int) {
	if !s.done {
		s.done = true
		if l := s.Header().Get("Location"); l != "" {
			if abs, err := filepath.Abs(s.a.Dir); err == nil && len(s.a.Dir) >= 2 {
				l = strings.ReplaceAll(l, url.QueryEscape(abs), url.QueryEscape(ownFolder))
				l = strings.ReplaceAll(l, url.QueryEscape(filepath.ToSlash(abs)), url.QueryEscape(ownFolder))
			}
			s.Header().Set("Location", s.a.hideDir(l))
		}
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *scrubWriter) Write(b []byte) (int, error) {
	if !s.done {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func (s *scrubWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
