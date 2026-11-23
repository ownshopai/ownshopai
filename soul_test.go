package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAboutShopFromSoul(t *testing.T) {
	h := newHarness(t)
	eq(t, "no soul yet", h.say(call("about_shop", "topic:"+e("hello")), "hello"),
		"Hello, this is Test Shop.\nAsk for a product (for example: do you have honey) or about hours and delivery.")
	soul, _ := os.ReadFile("sample/soul.txt")
	h.s.Set("soul", string(soul))
	h.s.Set("shop_name", "Green Leaf Organics")
	eq(t, "greeting", h.say(call("about_shop", "topic:"+e("who are you")), "who are you?"),
		"Hello, this is Green Leaf Organics. "+strings.TrimSpace(string(soul))+"\nAsk for a product (for example: do you have honey) or about hours and delivery.")
	greet := h.say(call("hello", ""), "Hello!")
	if !strings.HasPrefix(greet, "Hello, this is Green Leaf Organics.") {
		t.Fatalf("bare greeting: %q", greet)
	}
	eq(t, "off-topic picked about_shop", h.say(call("about_shop", "topic:"+e("weather")), "what is the weather today"), handoffText)
	// Soul and FAQ stay separate: a faq call never answers from the soul.
	eq(t, "faq ignores soul", h.say(call("faq", "question:"+e("do you give health advice")), "do you give health advice"), handoffText)
}

// The real model client against a fake OpenAI-compatible server: request shape, two tool calls
// in one answer, one merged cart reply, and the soul in the system prompt.
func TestStubProvider(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[
			{"id":"a","type":"function","function":{"name":"add_to_order","arguments":"{\"product\":\"raw honey\",\"quantity\":2}"}},
			{"id":"b","type":"function","function":{"name":"add_to_order","arguments":"{\"product\":\"coconut oil\",\"quantity\":1}"}}]}}]}`))
	}))
	defer srv.Close()
	h := newHarness(t)
	h.b.M = NewBrain(srv.URL, CompactSoul(strings.Repeat("We sell honey and oil. ", 40)))
	outs := h.b.Handle(context.Background(), Inbound{Phone: "+31600000009", Name: "Test Buyer", Text: "add 2 honey and 1 coconut oil"})
	if len(bodies) != 1 {
		t.Fatalf("want one model request per message, got %d", len(bodies))
	}
	if len(outs) != 1 || !strings.Contains(outs[0].Text, "2 x Raw honey 500g") || !strings.Contains(outs[0].Text, "1 x Cold-pressed coconut oil 500ml") {
		t.Fatalf("want one cart with both items, got %+v", outs)
	}
	b := bodies[0]
	kw, _ := b["chat_template_kwargs"].(map[string]any)
	if b["tool_choice"] != "required" || b["temperature"] != float64(0) || kw["enable_thinking"] != false {
		t.Fatalf("request params: tool_choice=%v temperature=%v kwargs=%v", b["tool_choice"], b["temperature"], b["chat_template_kwargs"])
	}
	if tl, _ := b["tools"].([]any); len(tl) != len(tools) {
		t.Fatalf("want %d tools, got %d", len(tools), len(tl))
	}
	msgs, _ := b["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	sys, _ := first["content"].(string)
	if first["role"] != "system" || !strings.HasPrefix(sys, systemPrompt+"\nShop context: We sell honey") || len(sys) > len(systemPrompt)+320 {
		t.Fatalf("system prompt: %q", sys)
	}
}

// One message may add several items, but never add and then confirm.
func TestMultiCallOnlyAdds(t *testing.T) {
	h := newHarness(t)
	h.buy()
	got := h.say(call("add_to_order", "product:"+e("honey")+",quantity:1")+" | "+call("confirm_order", "answer:"+e("yes")), "1 honey and yes")
	if strings.Contains(got, "confirmed") {
		t.Fatalf("confirmed inside a multi-call turn: %q", got)
	}
	o, _ := h.s.Order(1042)
	if o.Status != StCart {
		t.Fatalf("status %s", o.Status)
	}
}

// A bare YES with a cart ready to confirm is confirmed in Go, whatever tool the model would pick;
// without a ready cart it still goes to the model and hands off.
func TestYesConfirmsReadyCart(t *testing.T) {
	h := newHarness(t)
	eq(t, "yes with no cart", h.say(call("about_shop", "topic:"+e("order")), "YES"), handoffText)
	h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:2"), "add 2 raw honey")
	eq(t, "yes without a slot", h.say(call("about_shop", "topic:"+e("order")), "YES"), handoffText)
	h.say(call("pick_slot", "slot:"+e("1")), "slot 1")
	if got := h.say(call("about_shop", "topic:"+e("order")), "YES"); !strings.Contains(got, "Invoice INV-1042") {
		t.Fatalf("YES with a ready cart: %q", got)
	}
	if got := h.say(call("about_shop", "topic:"+e("order")), "yes"); !strings.Contains(got, handoffText) {
		t.Fatalf("second YES: %q", got)
	}
}
