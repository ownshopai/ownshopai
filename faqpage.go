package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// The FAQ page: the FAQ sheet of products.xlsx, edited in the browser. Every change rewrites the
// FAQ sheet (other sheets untouched) and re-reads the file, so the shop answers it at once.

// PutFAQs replaces the FAQ sheet with fs and re-reads the file.
func (sf *ShopFile) PutFAQs(fs []FAQ) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if modTime(sf.Path).IsZero() {
		return errors.New("upload your products file first")
	}
	f, err := excelize.OpenFile(sf.Path)
	if err != nil {
		return err
	}
	sh := ""
	for _, n := range f.GetSheetList() {
		if strings.EqualFold(strings.TrimSpace(n), sheetFAQ) {
			sh = n
		}
	}
	if sh != "" {
		if err := f.DeleteSheet(sh); err != nil {
			f.Close()
			return err
		}
	} else {
		sh = sheetFAQ
	}
	if _, err := f.NewSheet(sh); err != nil {
		f.Close()
		return err
	}
	rows := [][]any{{"question", "answer", "keywords"}}
	for _, q := range fs {
		rows = append(rows, []any{q.Question, q.Answer, q.Keywords})
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		f.SetSheetRow(sh, cell, &r)
	}
	err = f.Save()
	f.Close()
	if err != nil {
		return errors.New("could not write the file (is it open in Excel?): " + err.Error())
	}
	return sf.load() // replaces the FAQ table from the sheet, so the bot answers it at once
}

func cleanFAQ(r *http.Request) (FAQ, error) {
	q := FAQ{Question: strings.TrimSpace(r.FormValue("question")), Answer: strings.TrimSpace(r.FormValue("answer")),
		Keywords: strings.TrimSpace(r.FormValue("keywords"))}
	if q.Question == "" || q.Answer == "" {
		return q, errors.New("type a question and an answer")
	}
	return q, nil
}

func (a *App) faqPage(w http.ResponseWriter, r *http.Request) {
	fs, _ := a.S.FAQs()
	edit, _ := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64)
	a.page(w, "faq", map[string]any{"FAQs": fs, "Edit": edit, "Msg": r.URL.Query().Get("msg")})
}

func (a *App) faqRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /faq", a.faqPage)
	save := func(w http.ResponseWriter, r *http.Request, change func([]FAQ) ([]FAQ, error), ok string) {
		if a.File == nil {
			back(w, r, "/faq", "No products file.")
			return
		}
		fs, _ := a.S.FAQs()
		fs, err := change(fs)
		if err == nil {
			err = a.File.PutFAQs(fs)
		}
		if err != nil {
			back(w, r, "/faq", "Not saved: "+err.Error())
			return
		}
		back(w, r, "/faq", ok)
	}
	find := func(fs []FAQ, r *http.Request) (int, error) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		for i, f := range fs {
			if f.ID == id {
				return i, nil
			}
		}
		return -1, errors.New("that question is gone (the file changed?)")
	}
	m.HandleFunc("POST /faq", func(w http.ResponseWriter, r *http.Request) {
		save(w, r, func(fs []FAQ) ([]FAQ, error) {
			q, err := cleanFAQ(r)
			return append(fs, q), err
		}, "Added. The shop answers it from now on.")
	})
	m.HandleFunc("POST /faq/{id}", func(w http.ResponseWriter, r *http.Request) {
		save(w, r, func(fs []FAQ) ([]FAQ, error) {
			i, err := find(fs, r)
			if err != nil {
				return nil, err
			}
			q, err := cleanFAQ(r)
			fs[i] = q
			return fs, err
		}, "Saved.")
	})
	m.HandleFunc("POST /faq/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		save(w, r, func(fs []FAQ) ([]FAQ, error) {
			i, err := find(fs, r)
			if err != nil {
				return nil, err
			}
			return append(fs[:i], fs[i+1:]...), nil
		}, "Deleted.")
	})
}
