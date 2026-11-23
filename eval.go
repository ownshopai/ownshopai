package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	llmkit "github.com/muthuishere/toolnexus/golang"
)

// evalCase is one realistic customer message with the tool we expect and key args (substring match, lower case).
type evalCase struct {
	Msg, Tool string
	Args      map[string]string
}

// The cases were written before the first eval run and are not tuned to the model.
var evalCases = []evalCase{
	{"what time do you open on saturday", "faq", nil},
	{"are u open sunday", "faq", nil},
	{"do you deliver to Rotterdam?", "faq", nil},
	{"how much is delivery", "faq", nil},
	{"can i return the soap if i dont like it", "faq", nil},
	{"is the honey organic certified?", "faq", nil},
	{"wat r ur opening hours", "faq", nil},
	{"do you have honey?", "search_product", map[string]string{"query": "honey"}},
	{"got any almond butter", "search_product", map[string]string{"query": "almond"}},
	{"price of coconut oil?", "search_product", map[string]string{"query": "coconut"}},
	{"is millet flour in stock", "search_product", map[string]string{"query": "millet"}},
	{"do u sell green tea", "search_product", map[string]string{"query": "tea"}},
	{"how much for the lavender soap", "search_product", map[string]string{"query": "lavender"}},
	{"any gluten free flour?", "search_product", map[string]string{"query": "flour"}},
	{"add 2 raw honey", "add_to_order", map[string]string{"product": "honey", "quantity": "2"}},
	{"I want 3 jars of almond butter", "add_to_order", map[string]string{"product": "almond", "quantity": "3"}},
	{"1 coconut oil please", "add_to_order", map[string]string{"product": "coconut", "quantity": "1"}},
	{"pls add two herbal tea", "add_to_order", map[string]string{"product": "tea", "quantity": "2"}},
	{"can I order 4 beeswax soaps", "add_to_order", map[string]string{"product": "soap", "quantity": "4"}},
	{"put 1 olive oil in my basket", "add_to_order", map[string]string{"product": "olive", "quantity": "1"}},
	{"add 2 honey and 1 coconut oil", "add_to_order", map[string]string{"product": "honey", "quantity": "2"}},
	{"i'll take 5 oats", "add_to_order", map[string]string{"product": "oat", "quantity": "5"}},
	{"slot 2", "pick_slot", map[string]string{"slot": "2"}},
	{"Saturday 10-12", "pick_slot", map[string]string{"slot": "saturday"}},
	{"monday evening works", "pick_slot", map[string]string{"slot": "monday"}},
	{"the afternoon one on saturday pls", "pick_slot", map[string]string{"slot": "saturday"}},
	{"YES", "confirm_order", nil},
	{"yes please", "confirm_order", nil},
	{"Yes, confirm", "confirm_order", nil},
	{"send me the payment qr", "send_payment_qr", nil},
	{"how do i pay?", "send_payment_qr", nil},
	{"where is my order", "order_status", nil},
	{"has order 1042 shipped?", "order_status", nil},
	{"when will my stuff arrive", "order_status", nil},
	{"status of my order pls", "order_status", nil},
	{"hello", "about_shop", nil},
	{"what do you sell?", "about_shop", nil},
	{"who are you", "about_shop", nil},
	{"find customer Emma Novak", "find_customer", map[string]string{"name": "emma"}},
	{"what is the weather today", "none", nil},
	{"tell me a joke", "none", nil},
	{"who won the match last night", "none", nil},
	{"can you help me with my homework", "none", nil},
	{"what's bitcoin at", "none", nil},
}

// RunEval scores the model on evalCases and writes a scorecard. Returns tool hits and total.
// The first tool call is scored; the shop tools are replaced by no-ops so nothing touches the database.
func RunEval(m Brain, label string, w io.Writer) (int, int) {
	type score struct{ n, tool, args int }
	per := map[string]*score{}
	hits := 0
	var total time.Duration
	tk := evalToolkit()
	fmt.Fprintf(w, "== %s\n", label)
	for _, c := range evalCases {
		t0 := time.Now()
		res, err := m.Run(context.Background(), c.Msg, tk)
		total += time.Since(t0)
		got, raw := "(no call)", ""
		call := Call{Args: map[string]string{}}
		if len(res.ToolCalls) > 0 {
			call = Call{Name: res.ToolCalls[0].Name, Args: stringArgs(res.ToolCalls[0].Args)}
			got = call.Name
		}
		for i, tc := range res.ToolCalls {
			if i > 0 {
				raw += " + "
			}
			raw += callString(tc.Name, tc.Args)
		}
		if err != nil && raw == "" {
			raw = "ERROR " + err.Error()
		}
		if raw == "" {
			raw = "(no call) text: " + strings.Join(strings.Fields(res.Text), " ")
			if r := []rune(raw); len(r) > 100 {
				raw = string(r[:100]) + "..."
			}
		}
		sc := per[c.Tool]
		if sc == nil {
			sc = &score{}
			per[c.Tool] = sc
		}
		sc.n++
		mark, argsOK := "MISS", true
		if got == c.Tool {
			sc.tool++
			hits++
			mark = "ok  "
			for k, v := range c.Args {
				if !strings.Contains(strings.ToLower(call.Args[k]), v) {
					argsOK = false
				}
			}
			if argsOK {
				sc.args++
			} else {
				mark = "ARGS"
			}
		}
		fmt.Fprintf(w, "%s %-40q want %-15s got %s\n", mark, c.Msg, c.Tool, raw)
	}
	var names []string
	for k := range per {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Fprintf(w, "\n%-16s %6s %10s %10s\n", "tool", "cases", "tool ok", "tool+args")
	for _, k := range names {
		s := per[k]
		fmt.Fprintf(w, "%-16s %6d %10d %10d\n", k, s.n, s.tool, s.args)
	}
	fmt.Fprintf(w, "TOTAL tool choice %d/%d (%.0f%%), avg %d ms per message\n\n", hits, len(evalCases), 100*float64(hits)/float64(len(evalCases)),
		(total / time.Duration(len(evalCases))).Milliseconds())
	return hits, len(evalCases)
}

// evalToolkit has the shop tools' names and schemas with handlers that do nothing.
func evalToolkit() *llmkit.Toolkit {
	tk, err := llmkit.CreateToolkit(context.Background(), llmkit.Options{Builtins: false})
	if err != nil {
		panic(err)
	}
	for _, t := range tools {
		tk.Register(llmkit.NativeTool(t.Name, t.Desc, toolSchema(t),
			func(context.Context, map[string]any) (string, error) { return "ok", nil }))
	}
	return tk
}
