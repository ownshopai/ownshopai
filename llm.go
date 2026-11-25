package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	llmkit "github.com/muthuishere/toolnexus/golang"
)

// Brain runs one customer message through the model with the shop's tools.
type Brain interface {
	Run(ctx context.Context, prompt string, tk *llmkit.Toolkit) (llmkit.RunResult, error)
}

type toolDef struct {
	Name, Desc string
	Params     []string
	Required   []string
}

var intParams = map[string]bool{"quantity": true}

// Tool order matters for the small local model: faq first stops "opening hours" falling to find_customer.
var tools = []toolDef{
	{"faq", "Answer general questions about the shop: hours, open, delivery, deliver, returns, refund, organic, certified.", []string{"question"}, []string{"question"}},
	{"search_product", "Search products. Use when the customer asks if the shop has a product, its price or stock, e.g. do you have honey.", []string{"query"}, []string{"query"}},
	{"add_to_order", "Add a product and quantity to the order when the customer wants to buy, e.g. 1 coconut oil please.", []string{"product", "quantity"}, []string{"product", "quantity"}},
	{"pick_slot", "Pick a delivery slot (day or time or slot number), e.g. Saturday 10-12.", []string{"slot"}, []string{"slot"}},
	{"confirm_order", "Confirm the order when the customer says yes.", []string{"answer"}, []string{"answer"}},
	{"send_payment_qr", "Send the payment QR code so the customer can pay.", []string{"order_id"}, nil},
	{"order_status", "Get the status of the customer's order, e.g. where is my order.", []string{"order_id"}, nil},
	{"about_shop", "Greeting or who the shop is: hello, hi, who are you, what kind of shop is this.", []string{"topic"}, []string{"topic"}},
	{"none", "Use for anything else, e.g. weather, jokes, news.", []string{"reason"}, []string{"reason"}},
	{"find_customer", "Shop owner only: look up a customer record by the word customer plus a name, e.g. find customer Emma.", []string{"name"}, []string{"name"}},
}

// systemPrompt is the instruction the local model gets for every customer message.
const systemPrompt = "You are the WhatsApp assistant of a small organic shop. For every customer message call exactly one tool. " +
	"Use none for anything unrelated to the shop."

// DefaultModelPath is the bundled local model (Qwen3.5 0.8B, Q4_K_M, about 533 MB).
const DefaultModelPath = "models/qwen35-0.8b-q4km.gguf"

// requestParams go into every chat request: always call a tool, no sampling noise, and no
// thinking block (Qwen3.5's chat template reads enable_thinking).
var requestParams = map[string]any{
	"tool_choice":          "required",
	"temperature":          0,
	"max_tokens":           120,
	"chat_template_kwargs": map[string]any{"enable_thinking": false},
}

// NewBrain builds the model client for an OpenAI-compatible endpoint (llama-server, base URL
// without /v1). soul is an optional compact shop description added to the system prompt.
func NewBrain(baseURL, soul string) Brain {
	sys := systemPrompt
	if soul != "" {
		sys += "\nShop context: " + soul
	}
	return llmkit.CreateClient(llmkit.ClientOptions{
		BaseURL: strings.TrimRight(baseURL, "/") + "/v1", Style: llmkit.StyleOpenAI, Model: "local", APIKey: "local",
		SystemPrompt: sys, MaxTurns: 1, MaxToolConcurrency: 1, RequestParams: requestParams,
		BodyTransform: func(body map[string]any) map[string]any { body["tools"] = toolsWire; return body },
	})
}

// toolsWire is the tools array with its keys in a fixed, readable order (type, properties,
// required). The chat template prints the schema as JSON into the prompt, so key order changes
// what the small model reads; this order is the one the reference measurement used.
var toolsWire = func() json.RawMessage {
	var b strings.Builder
	q := func(s string) string { v, _ := json.Marshal(s); return string(v) }
	b.WriteString("[")
	for i, t := range tools {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"function","function":{"name":%s,"description":%s,"parameters":{"type":"object","properties":{`, q(t.Name), q(t.Desc))
		for j, p := range t.Params {
			if j > 0 {
				b.WriteString(",")
			}
			typ := "string"
			if intParams[p] {
				typ = "integer"
			}
			fmt.Fprintf(&b, `%s:{"type":%s}`, q(p), q(typ))
		}
		b.WriteString(`},"required":[`)
		for j, r := range t.Required {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(q(r))
		}
		b.WriteString("]}}}")
	}
	b.WriteString("]")
	return json.RawMessage(b.String())
}()

// StartBrain starts llama-server with the configured model and returns the brain and a stop function.
func StartBrain(s *Store) (Brain, func(), error) {
	path := s.Get("model_path")
	if path == "" {
		path = DefaultModelPath
	}
	l, err := StartLlama(helperOverride(s, "llama_server"), findFile(path))
	if err != nil {
		return nil, func() {}, err
	}
	soul := ""
	if s.Get("soul_in_prompt") == "on" {
		soul = CompactSoul(s.Get("soul"))
	}
	return NewBrain(l.URL, soul), l.Stop, nil
}

// Llama is a llama-server child process.
type Llama struct {
	URL string
	cmd *exec.Cmd
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// StartLlama spawns llama-server on a free local port and waits until it is healthy.
// bin "" means the llama-server bundled inside ownshop.
func StartLlama(bin, model string) (*Llama, error) {
	bin, err := helper("llama-server", bin)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("model file %s: %w", model, err)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := []string{"-m", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--jinja", "-c", "4096", "--log-disable"}
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		// llama-server uses every core by default; on phones (big.LITTLE) and busy VMs its spinning threads then
		// wait on each other: 0.3 tokens/s with 18 threads vs 93 with 4 in a linux/arm64 container.
		args = append(args, "-t", strconv.Itoa(min(4, runtime.NumCPU())))
	}
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	l := &Llama{URL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := http.Get(l.URL + "/health"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return l, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	l.Stop()
	return nil, fmt.Errorf("llama-server did not become healthy on port %d", port)
}

func (l *Llama) Stop() {
	if l != nil && l.cmd != nil && l.cmd.Process != nil {
		l.cmd.Process.Kill()
		l.cmd.Wait()
	}
}

// Call is one tool call with its arguments as strings.
type Call struct {
	Name string
	Args map[string]string
}

func stringArgs(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != nil {
			out[k] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out
}

// callString shows a tool call for logs: name(key=value, ...), keys sorted.
func callString(name string, args map[string]any) string {
	a := stringArgs(args)
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = k + "=" + a[k]
	}
	return name + "(" + strings.Join(keys, ", ") + ")"
}

// CompactSoul trims the owner's soul text to one short paragraph for the model prompt.
func CompactSoul(soul string) string {
	s := strings.Join(strings.Fields(soul), " ")
	if len(s) > 300 {
		s = s[:300]
		if i := strings.LastIndexAny(s, ".!?"); i > 100 {
			s = s[:i+1]
		}
	}
	return s
}
