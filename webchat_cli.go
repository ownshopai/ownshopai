package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"time"
)

// parseAny parses flags placed before, between or after the positional arguments.
func parseAny(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// webuserCmd: ownshop webuser add <email or phone> <password> [--name "Display"] | list | delete <email or phone>
func webuserCmd(args []string) error {
	fs := flag.NewFlagSet("webuser", flag.ExitOnError)
	dbPath := fs.String("db", "ownshop.db", "SQLite database file")
	name := fs.String("name", "", "display name (add)")
	pos := parseAny(fs, args)
	if len(pos) == 0 {
		return errors.New("usage: ownshop webuser add <email or phone> <password> [--name ..] | list | delete <email or phone> [--db ownshop.db]")
	}
	s, err := OpenStore(*dbPath)
	if err != nil {
		return err
	}
	switch {
	case pos[0] == "add" && len(pos) == 3:
		if err := s.AddWebUser(pos[1], pos[2], *name); err != nil {
			return err
		}
		fmt.Println("added", pos[1])
	case pos[0] == "list":
		us, err := s.WebUsers()
		if err != nil {
			return err
		}
		for _, u := range us {
			fmt.Printf("%d\t%s\t%s\t%s\n", u.ID, u.Username, u.DisplayName, u.Created)
		}
	case pos[0] == "delete" && len(pos) == 2:
		if err := s.DeleteWebUser(pos[1]); err != nil {
			return err
		}
		fmt.Println("deleted", pos[1])
	default:
		return errors.New("usage: ownshop webuser add <email or phone> <password> [--name ..] | list | delete <email or phone>")
	}
	return nil
}

// userCmd: ownshop user block|unblock <id or phone> | list
func userCmd(args []string) error {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dbPath := fs.String("db", "ownshop.db", "SQLite database file")
	pos := parseAny(fs, args)
	s, err := OpenStore(*dbPath)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 2 && (pos[0] == "block" || pos[0] == "unblock"):
		if err := s.SetBlocked(pos[1], pos[0] == "block"); err != nil {
			return err
		}
		fmt.Println(pos[0]+"ed", pos[1])
	case len(pos) == 1 && pos[0] == "list":
		cs, err := s.Customers()
		if err != nil {
			return err
		}
		for _, c := range cs {
			b := ""
			if c.Blocked {
				b = "blocked"
			}
			fmt.Printf("%d\t%s\t%s\t%s\t%d orders\t%s\n", c.ID, c.Phone, c.Name, c.Channel, c.Orders, b)
		}
	default:
		return errors.New("usage: ownshop user block|unblock <id or phone> | list [--db ownshop.db]")
	}
	return nil
}

// sayCmd logs in to a running ownshop as a web chat test customer, sends one message (or a
// location) and prints the shop's replies until it goes quiet.
func sayCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	user := fs.String("user", "", "web chat login: email or phone")
	pw := fs.String("password", "", "web chat password")
	base := fs.String("url", "http://127.0.0.1:2007", "OwnShop AI address")
	loc := fs.String("location", "", "send a location instead of text: lat,lng")
	quiet := fs.Duration("quiet", 3*time.Second, "stop after this long without a new reply")
	maxWait := fs.Duration("timeout", 60*time.Second, "give up after this long")
	text := strings.Join(parseAny(fs, args), " ")
	if *user == "" || (text == "" && *loc == "") {
		return errors.New(`usage: ownshop say --user <email or phone> --password <pw> [--url http://127.0.0.1:2007] [--location lat,lng] "text"`)
	}
	body := map[string]any{"text": text}
	if *loc != "" {
		la, ln, ok := strings.Cut(*loc, ",")
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(la), 64)
		lng, e2 := strconv.ParseFloat(strings.TrimSpace(ln), 64)
		if !ok || e1 != nil || e2 != nil {
			return errors.New("--location must be lat,lng")
		}
		body["lat"], body["lng"] = lat, lng
	}
	*base = strings.TrimRight(*base, "/")
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	call := func(method, path string, in, res any) error {
		var rd io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, *base+path, rd)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 {
			return fmt.Errorf("%s %s: HTTP %d %s", method, path, r.StatusCode, strings.TrimSpace(string(raw)))
		}
		if res != nil {
			return json.Unmarshal(raw, res)
		}
		return nil
	}
	if err := call("POST", "/webchat/login", map[string]string{"username": *user, "password": *pw}, nil); err != nil {
		return err
	}
	var page struct{ Messages []WebMsg }
	if err := call("GET", "/webchat/messages", nil, &page); err != nil {
		return err
	}
	var last int64
	for _, m := range page.Messages {
		last = m.ID
	}
	t0 := time.Now()
	if err := call("POST", "/webchat/send", body, nil); err != nil {
		return err
	}
	lastReply, replies := time.Now(), 0
	for time.Since(t0) < *maxWait {
		time.Sleep(250 * time.Millisecond)
		if err := call("GET", "/webchat/messages?after="+strconv.FormatInt(last, 10), nil, &page); err != nil {
			return err
		}
		for _, m := range page.Messages {
			last = m.ID
			if m.Direction != "out" {
				continue
			}
			replies++
			lastReply = time.Now()
			el := time.Since(t0).Seconds()
			if m.Image != "" {
				fmt.Fprintf(out, "shop> [image] %s%s  (%.2fs)\n", *base, m.Image, el)
			}
			if m.Text != "" {
				for _, line := range strings.Split(m.Text, "\n") {
					fmt.Fprintln(out, "shop> "+line)
				}
				fmt.Fprintf(out, "      (%.2fs)\n", el)
			}
		}
		if replies > 0 && time.Since(lastReply) >= *quiet {
			return nil
		}
	}
	if replies == 0 {
		return fmt.Errorf("no reply within %s", *maxWait)
	}
	return nil
}

func runCLI(cmd string, args []string) bool {
	var err error
	switch cmd {
	case "webuser":
		err = webuserCmd(args)
	case "user":
		err = userCmd(args)
	case "say":
		err = sayCmd(args, os.Stdout)
	default:
		return false
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownshop "+cmd+": "+err.Error())
		os.Exit(1)
	}
	return true
}
