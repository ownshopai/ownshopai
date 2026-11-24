package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Common questions answered in Go from what the shop already knows (hours, address, prices,
// free times, stock, payment methods), before the model and before a hand-off, so a small model
// cannot turn "are you open today" into "the shop will reply shortly". The owner's FAQ sheet still
// wins when it has a match. One table; add a row to extend it.

type builtinRule struct {
	name string
	any  []string // a word or phrase of the message (normalised) that triggers the rule
	only []string // the whole message is one of these
	need func(b *Bot, w msgWords) bool
	say  func(b *Bot, c Customer, text string) []Out
	own  bool // the owner's FAQ is not consulted first (status, people, thanks)
}

var builtinRules []builtinRule

func init() { builtinRules = rulesTable() }

func rulesTable() []builtinRule {
	return []builtinRule{
		{name: "thanks", own: true, only: []string{"thanks", "thank you", "thank you so much", "thanks a lot", "thx", "ty", "tq", "ok", "okay", "ok thanks",
			"ok thank you", "okay thanks", "bye", "bye bye", "good night", "great", "cool", "fine", "shukriya", "dhanyavad", "nandri"},
			say: func(b *Bot, c Customer, text string) []Out {
				return []Out{{Text: "You're welcome! Write any time."}}
			}},
		{name: "human", own: true, any: []string{"human", "real person", "a person", "talk to", "speak to", "call me", "contact", "phone number", "your number", "owner", "manager", "agent"},
			say: func(b *Bot, c Customer, text string) []Out {
				return b.handoffSay(c, text, "asked for a person", "The shop will reply to you here shortly.")
			}},
		{name: "status", own: true, any: []string{"my order", "my booking", "where is order", "order number", "booking number", "order status", "booking status", "status", "where is my", "track"},
			say: func(b *Bot, c Customer, text string) []Out { return b.run(c, Call{Name: "order_status"}, text) }},
		{name: "cancel", any: []string{"cancel", "reschedule", "postpone", "change my time", "change the time", "change time", "another time", "different time"},
			say: func(b *Bot, c Customer, text string) []Out {
				// Customers cannot cancel or move a booking by chat yet: the owner does it on the Calendar.
				return b.handoffSay(c, text, "cancel or change", "To cancel or change your time, the shop will help you here shortly.")
			}},
		{name: "payment", any: []string{"pay", "payment", "upi", "gpay", "google pay", "phonepe", "paytm", "card", "cash", "bank transfer", "how to pay"},
			say: func(b *Bot, c Customer, text string) []Out {
				if o, _ := b.S.LatestOrder(c.ID); o != nil && o.Status == StAwaiting {
					return b.run(c, Call{Name: "send_payment_qr"}, text) // an order waits: say how to pay it
				}
				var ways []string
				if v := b.S.Get("upi_id"); v != "" {
					ways = append(ways, "UPI ("+v+")")
				}
				if b.S.Get("qr_path") != "" {
					ways = append(ways, "a payment QR code")
				}
				if b.S.Get("pay_link") != "" {
					ways = append(ways, "a payment link")
				}
				if b.S.Get("pay_note") != "" {
					ways = append(ways, "bank transfer")
				}
				if len(ways) == 0 {
					return b.handoffSay(c, text, "payment: no method set", "The shop will tell you how to pay here shortly.")
				}
				return []Out{{Text: "We accept " + joinAnd(ways) + ". You get the details after your order is confirmed."}}
			}},
		{name: "delivery", any: []string{"deliver", "delivery", "shipping", "ship", "courier", "home delivery"},
			say: func(b *Bot, c Customer, text string) []Out {
				return b.handoffSay(c, text, "delivery question", "Ask us about delivery to your area: the shop will reply here shortly.")
			}},
		{name: "slots", any: []string{"slot", "slots", "free time", "free times", "next free", "available time", "available times", "book today", "book tomorrow",
			"appointment today", "appointment tomorrow", "free today", "free tomorrow", "earliest"},
			say: func(b *Bot, c Customer, text string) []Out { return b.nextFree(c, text) }},
		{name: "hours", any: []string{"hours", "open", "opening", "close", "closing", "closed", "timing", "timings", "when are you", "working days", "sunday",
			"saturday", "holiday", "kab khulta", "kab"},
			say: func(b *Bot, c Customer, text string) []Out { return []Out{{Text: b.hoursText(text)}} }},
		{name: "address", any: []string{"where", "address", "location", "located", "directions", "map", "kahan", "kidhar"},
			say: func(b *Bot, c Customer, text string) []Out {
				if a := strings.TrimSpace(b.S.Get("address")); a != "" {
					return []Out{{Text: "You can find us at: " + a}}
				}
				return b.handoff(c, text, "address: none set")
			}},
		{name: "duration", any: []string{"how long", "duration", "minutes", "kitni der", "kitna time", "takes"},
			say: func(b *Bot, c Customer, text string) []Out {
				if p := b.namedItem(text); p != nil && p.IsService() {
					return []Out{{Text: fmt.Sprintf("%s takes %d min. To book, reply for example: book %s", p.Name, p.Minutes, p.Name)}}
				}
				return b.catalog(c, text)
			}},
		{name: "price", any: []string{"price", "prices", "cost", "how much", "rate", "rates", "charge", "charges", "fee", "fees", "kitna", "kitne", "kitni"},
			say: func(b *Bot, c Customer, text string) []Out {
				if p := b.namedItem(text); p != nil { // the search reply with that one item
					return []Out{{Text: render("search", map[string]any{"Products": []Product{*p}, "Cur": b.cur()})}}
				}
				return b.catalog(c, text)
			}},
		{name: "stock", any: []string{"stock", "in stock", "available", "availability"},
			need: func(b *Bot, w msgWords) bool { return b.namedItem(w.text) != nil },
			say: func(b *Bot, c Customer, text string) []Out {
				p := b.namedItem(text)
				if p.IsService() {
					return b.nextFree(c, "book "+p.Name)
				}
				if p.Stock > 0 {
					return []Out{{Text: fmt.Sprintf("Yes, %s is in stock (%d). To order, reply for example: add 1 %s", p.Name, p.Stock, p.Name)}}
				}
				return []Out{{Text: p.Name + " is out of stock right now."}}
			}},
	}
}

// msgWords is a message lowercased with punctuation stripped, padded so phrases match whole words.
type msgWords struct{ text, padded, joined string }

func normalise(text string) msgWords {
	j := strings.Join(words(text), " ")
	return msgWords{text: text, padded: " " + j + " ", joined: j}
}

func (w msgWords) has(p string) bool { return strings.Contains(w.padded, " "+p+" ") }

// builtinMatch is the first rule the message triggers.
func (b *Bot) builtinMatch(text string) *builtinRule {
	w := normalise(text)
	for i := range builtinRules {
		r := &builtinRules[i]
		hit := false
		for _, o := range r.only {
			hit = hit || w.joined == o
		}
		for _, p := range r.any {
			hit = hit || w.has(p)
		}
		if hit && (r.need == nil || r.need(b, w)) {
			return r
		}
	}
	return nil
}

// builtin answers a common question; the owner's FAQ sheet goes first when it matches.
func (b *Bot) builtin(c Customer, text string) ([]Out, string, bool) {
	r := b.builtinMatch(text)
	if r == nil {
		return nil, "", false
	}
	if !r.own {
		if f := b.ownerFAQ(text); f != "" {
			return []Out{{Text: render("faq", map[string]any{"Answer": f})}}, "faq", true
		}
	}
	return r.say(b, c, text), r.name, true
}

// weakWords match too much to let an owner FAQ row win on them alone.
var weakWords = map[string]bool{"where": true, "when": true, "time": true, "what": true, "how": true, "open": false}

// ownerFAQ is the owner's best FAQ answer sharing a real word with the message, or "".
func (b *Bot) ownerFAQ(text string) string {
	a, _ := b.ownerFAQScore(text)
	return a
}

// ownerFAQScore is ownerFAQ plus how many real words matched.
func (b *Bot) ownerFAQScore(text string) (string, int) {
	fs, _ := b.S.FAQs()
	want := keySet(text)
	for k := range want {
		if weakWords[k] {
			delete(want, k)
		}
	}
	best, score := "", 0
	for _, f := range fs {
		n := 0
		for k := range keySet(f.Keywords + " " + f.Question) {
			if want[k] {
				n++
			}
		}
		if n > score {
			best, score = f.Answer, n
		}
	}
	return best, score
}

// strongOwnerFAQ is an owner FAQ answer that clearly fits: two real words in common, or one word
// that is in the row's own keywords and names no product. It is answered in Go before the model.
func (b *Bot) strongOwnerFAQ(text string) string {
	a, n := b.ownerFAQScore(text)
	if a == "" {
		return ""
	}
	if n >= 2 || len(b.matchProducts(text)) == 0 {
		return a
	}
	return ""
}

func (b *Bot) handoffSay(c Customer, text, reason, say string) []Out {
	b.debug("hand-off: %s", reason)
	b.S.Handoff(c.Phone, text, reason)
	return []Out{{Text: say, kind: "handoff"}}
}

func joinAnd(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// ruleWords never name an item ("price of the facial": only "facial" counts).
var ruleWords = map[string]bool{}

func init() {
	for _, r := range rulesTable() {
		for _, p := range append(r.any, r.only...) {
			for _, w := range strings.Fields(p) {
				ruleWords[w] = true
			}
		}
	}
	for _, w := range []string{"book", "service", "services", "product", "products", "today", "tomorrow", "of", "is", "there", "will", "it", "much"} {
		ruleWords[w] = true
	}
}

// namedItem is the active product or service the message names, if any.
func (b *Bot) namedItem(text string) *Product {
	var rest []string
	for _, w := range words(text) {
		if !ruleWords[w] {
			rest = append(rest, w)
		}
	}
	if len(keySet(strings.Join(rest, " "))) == 0 {
		return nil
	}
	ps := b.matchProducts(strings.Join(rest, " "))
	if len(ps) == 0 {
		return nil
	}
	return &ps[0]
}

// hoursText is the weekly hours and whether the shop is open today; date overrides count.
var longDay = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func (b *Bot) hoursText(text string) string {
	sc := b.S.Schedule()
	now := b.now()
	today := startOfDay(now)
	out := "Our hours: " + sc.Avail.weekText() + "."
	for i := range weekOrder { // "are you open sunday": answer that day first
		if strings.Contains(" "+strings.Join(words(text), " ")+" ", " "+strings.ToLower(longDay[i])+" ") {
			if rs := sc.Week[(i+1)%7]; len(rs) > 0 {
				out = "Yes, on " + longDay[i] + " we are open " + rangesText(rs) + ".\n" + out
			} else {
				out = "Sorry, we are closed on " + longDay[i] + "s.\n" + out
			}
			break
		}
	}
	if rs := sc.RangesOn(today); len(rs) > 0 {
		out += "\nOpen today " + rangesText(rs) + "."
	} else {
		out += "\nClosed today."
		for i := 1; i <= 14; i++ {
			d := today.AddDate(0, 0, i)
			if rs := sc.RangesOn(d); len(rs) > 0 {
				out = strings.TrimSuffix(out, ".") + ", next open " + d.Format("Mon 2 Jan") + " " + ampm(rs[0][0]) + "."
				break
			}
		}
	}
	return out
}

// nextFree lists the next free times for the named service (or the first one).
func (b *Bot) nextFree(c Customer, text string) []Out {
	p := b.namedItem(text)
	if p == nil || !p.IsService() {
		for _, x := range b.offer() {
			if x.IsService() {
				x := x
				p = &x
				break
			}
		}
	}
	if p == nil {
		return b.handoff(c, text, "slots: no service")
	}
	now := b.now()
	sc := b.S.Schedule()
	for i := 0; i <= sc.Ahead; i++ {
		d := startOfDay(now).AddDate(0, 0, i)
		ts := b.S.FreeTimes(d, now, p.Minutes, 0)
		if len(ts) == 0 {
			continue
		}
		if len(ts) > 6 {
			ts = ts[:6]
		}
		return []Out{{Text: fmt.Sprintf("Next free times for %s, %s: %s.\nTo book, reply for example: book %s", p.Name, dayLabel(d, now), strings.Join(ts, ", "), p.Name)}}
	}
	return b.handoffSay(c, text, "slots: none free", "Sorry, no free times in the next days. The shop will reply shortly.")
}

// weekText is the weekly hours alone: "Mon–Fri 9:00 AM–6:00 PM; Sat 10:00 AM–2:00 PM".
func (a Avail) weekText() string {
	s := a.Summary(30, 7)
	if i := strings.Index(s, " · "); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- catalog, greetings and "book X", in Go ----

// offer is what the shop sells: services for a services shop, products for a products shop.
func (b *Bot) offer() []Product {
	ps, _ := b.S.Sellable()
	return ps
}

func (b *Bot) catalog(c Customer, text string) []Out {
	ps := b.offer()
	if len(ps) == 0 {
		return b.handoff(c, text, "catalog: nothing to sell")
	}
	more := 0
	if len(ps) > 20 {
		ps, more = ps[:20], len(ps)-20
	}
	return []Out{{Text: render("search", map[string]any{"Products": ps, "Cur": b.cur(), "More": more})}}
}

var catalogPhrases = []string{"services", "service", "products", "what do you have", "what do you offer", "what you offer", "what you have", "offer",
	"menu", "price list", "list", "available", "catalog", "catalogue", "rates", "what can i book", "what can i order", "what do you sell", "items"}

// isCatalogAsk: the message asks what the shop offers, without naming an item.
func (b *Bot) isCatalogAsk(text string) bool {
	w := normalise(text)
	for _, p := range catalogPhrases {
		if w.has(p) {
			return b.namedItem(text) == nil
		}
	}
	return false
}

var greetWords = []string{"good morning", "good afternoon", "good evening", "hello there", "hi there", "hello", "hi", "hii", "hiii", "hey", "hallo",
	"hola", "bonjour", "vanakkam", "namaste", "namaskar", "salaam", "hai"}

var smallTalk = map[string]bool{"": true, "how are you": true, "how r u": true, "how are u": true, "sir": true, "madam": true, "there": true, "all": true}

// splitGreeting takes a greeting off the front: ("hi what are the services", true) -> "what are the services".
func splitGreeting(text string) (string, bool) {
	j := strings.Join(words(text), " ")
	for _, g := range greetWords {
		if j == g || strings.HasPrefix(j, g+" ") {
			rest := strings.TrimSpace(strings.TrimPrefix(j, g))
			if smallTalk[rest] {
				return "", true
			}
			return rest, true
		}
	}
	return text, false
}

func (b *Bot) greet() []Out {
	hint := ""
	if b.S.Get("sells") != "products" && b.S.Get("sells") != "" {
		hint = "Reply services to see what you can book."
	}
	soul := strings.TrimSpace(b.S.Get("soul"))
	return []Out{{Text: render("about", map[string]any{"Shop": b.S.Get("shop_name"), "Soul": soul, "Hint": hint})}}
}

var bookLead = map[string]bool{"book": true, "i": true, "want": true, "to": true, "a": true, "an": true, "appointment": true, "for": true, "please": true,
	"need": true, "would": true, "like": true, "id": true, "get": true, "me": true, "slot": true, "session": true}

// bookIntent: "book consultation", "I want a haircut" -> the service (or product) named.
func (b *Bot) bookIntent(text string) *Product {
	ws := words(text)
	if len(ws) < 2 || !(ws[0] == "book" || (ws[0] == "i" && (ws[1] == "want" || ws[1] == "need" || ws[1] == "would")) || ws[0] == "want") {
		return nil
	}
	var rest []string
	for _, w := range ws {
		if !bookLead[w] {
			rest = append(rest, w)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	ps := b.matchProducts(strings.Join(rest, " "))
	if len(ps) == 0 {
		return nil
	}
	return &ps[0]
}

// goFirst answers in Go what needs no model: a greeting, "what do you have", "book X", common questions.
func (b *Bot) goFirst(c Customer, text string) (tool, calls string, outs []Out, ok bool) {
	if o, _ := b.S.Cart(c.ID); o != nil && len(o.Items) > 0 {
		// Mid-order: "slot 1", "2 more" and the like belong to the order flow and the model;
		// only plain questions (how to pay, hours, thanks...) are answered here.
		if r := b.builtinMatch(text); r != nil && r.name != "slots" && r.name != "stock" && r.name != "price" && r.name != "duration" {
			outs, name, _ := b.builtin(c, text)
			return name, "go:builtin_" + name, outs, true
		}
		if f := b.strongOwnerFAQ(text); f != "" && b.builtinMatch(text) == nil && !looksLikeOrderReply(text) {
			return "faq", "go:owner_faq", []Out{{Text: render("faq", map[string]any{"Answer": f})}}, true
		}
		return "", "", nil, false
	}
	rest, greeted := splitGreeting(text)
	if greeted && rest == "" {
		return "about_shop", "go:greeting", b.greet(), true
	}
	if p := b.bookIntent(rest); p != nil {
		return "add_to_order", "go:book", b.add(c, p.Name, "1", rest), true
	}
	if outs, name, ok := b.builtin(c, rest); ok {
		return name, "go:builtin_" + name, outs, true
	}
	if b.isCatalogAsk(rest) {
		return "search_product", "go:catalog", b.catalog(c, rest), true
	}
	if f := b.strongOwnerFAQ(rest); f != "" {
		return "faq", "go:owner_faq", []Out{{Text: render("faq", map[string]any{"Answer": f})}}, true
	}
	if greeted {
		return "", "", nil, false
	}
	return "", "", nil, false
}

// fallback is tried before a hand-off when the model's faq or search found nothing.
func (b *Bot) fallback(c Customer, text string) ([]Out, bool) {
	if outs, _, ok := b.builtin(c, text); ok {
		return outs, true
	}
	if f := b.ownerFAQ(text); f != "" {
		return []Out{{Text: render("faq", map[string]any{"Answer": f})}}, true
	}
	if b.isCatalogAsk(text) {
		return b.catalog(c, text), true
	}
	return nil, false
}

func priceText(cur string, v float64) string { return fmt.Sprintf("%s %.2f", cur, v) }

// looksLikeOrderReply is a short reply that belongs to the order flow: "slot 1", "yes", "2", "3 more".
func looksLikeOrderReply(text string) bool {
	w := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(w) == 0 || len(w) > 3 {
		return len(w) == 0
	}
	for _, x := range w {
		switch x {
		case "yes", "y", "ok", "okay", "no", "slot", "more", "less", "confirm", "cancel":
			return true
		}
		if _, err := strconv.Atoi(x); err == nil {
			return true
		}
	}
	return false
}
