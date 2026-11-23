package main

// UCP over MCP: a plain JSON-RPC 2.0 endpoint at POST /ucp/mcp (MCP streamable HTTP, JSON responses
// only, no SSE stream). Tool names and arguments follow the UCP MCP binding (v2026-08-25,
// source/services/shopping/mcp.openrpc.json): every tool takes "meta" and either "id", "checkout"
// or "catalog"; results carry the UCP object as structuredContent plus the same JSON as text.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

var (
	mcpMeta = map[string]any{"type": "object", "description": "UCP request metadata, e.g. {\"ucp-agent\":{\"profile\":\"https://...\"},\"idempotency-key\":\"...\"}"}
	mcpID   = map[string]any{"type": "string"}
	mcpCO   = map[string]any{"type": "object", "description": "UCP checkout: line_items [{item:{id}, quantity}], buyer {first_name, last_name, email, phone_number}, " +
		"fulfillment {methods:[{type:\"shipping\", destinations:[postal address], groups:[{selected_option_id}]}]}, " +
		"\"com.deemwar.ownshop.booking\" {start:\"2026-11-21T10:00\"} for services"}
)

func mcpTools() []any {
	t := func(name, desc string, schema map[string]any) map[string]any {
		return map[string]any{"name": name, "description": desc, "inputSchema": schema}
	}
	return []any{
		t("search_catalog", "Search the shop's products and services (price, stock, photo, free booking times).",
			obj(map[string]any{"meta": mcpMeta, "catalog": map[string]any{"type": "object", "description": "{query, filters:{price:{min,max}}, pagination:{limit, cursor}}; amounts in minor units"}}, "catalog")),
		t("create_checkout", "Start a checkout session.", obj(map[string]any{"meta": mcpMeta, "checkout": mcpCO}, "checkout")),
		t("get_checkout", "Get a checkout session.", obj(map[string]any{"meta": mcpMeta, "id": mcpID}, "id")),
		t("update_checkout", "Replace a checkout's line items, and buyer, fulfillment or booking when given.", obj(map[string]any{"meta": mcpMeta, "id": mcpID, "checkout": mcpCO}, "id", "checkout")),
		t("complete_checkout", "Place the order. Payment is offline: the result carries the order id, invoice and how to pay; it stays unpaid until the shop confirms the payment.",
			obj(map[string]any{"meta": mcpMeta, "id": mcpID, "checkout": map[string]any{"type": "object", "description": "optional {payment:{instruments:[{id, handler_id, type}]}} naming one of the shop's handlers"}}, "id")),
		t("cancel_checkout", "Cancel a checkout session that is not completed.", obj(map[string]any{"meta": mcpMeta, "id": mcpID}, "id")),
		t("get_order", "Get an order placed through this endpoint. Besides id, give the buyer's phone_number or email, or the checkout_id it came from.",
			obj(map[string]any{"meta": mcpMeta, "id": mcpID, "checkout_id": mcpID,
				"buyer": map[string]any{"type": "object", "description": "{phone_number} or {email} used at checkout"}}, "id")),
	}
}

func (a *App) ucpMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ucpJSON(w, http.StatusRequestEntityTooLarge, rpcError(nil, -32600, "request too large"))
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		ucpJSON(w, http.StatusBadRequest, rpcError(nil, -32700, "send one JSON-RPC 2.0 request (batches are not supported)"))
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" { // a notification: nothing to answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := mcpVersions[0]
		for _, s := range mcpVersions {
			if s == p.ProtocolVersion {
				v = s
			}
		}
		ucpJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "ownshop-ucp", "version": ucpVersion},
			"instructions": "UCP " + ucpVersion + " shopping tools for " + a.S.Get("shop_name") +
				". search_catalog, then create_checkout / update_checkout until status is ready_for_complete, then complete_checkout. Payment is offline (UPI, bank transfer or payment link); the shop confirms it by hand.",
		}})
	case "ping":
		ucpJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
	case "tools/list":
		ucpJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": mcpTools()}})
	case "tools/call":
		a.mcpCall(w, r, req)
	default:
		ucpJSON(w, 200, rpcError(req.ID, -32601, "method not found: "+req.Method))
	}
}

func (a *App) mcpCall(w http.ResponseWriter, r *http.Request, req rpcReq) {
	var p struct {
		Name string `json:"name"`
		Args struct {
			ID         string          `json:"id"`
			CheckoutID string          `json:"checkout_id"`
			Checkout   json.RawMessage `json:"checkout"`
			Catalog    json.RawMessage `json:"catalog"`
			Buyer      ucpBuyer        `json:"buyer"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		ucpJSON(w, 200, rpcError(req.ID, -32602, "invalid params"))
		return
	}
	base, args := baseURL(r), p.Args
	var out any
	var err error
	switch p.Name {
	case "search_catalog":
		out, err = a.ucpSearch(args.Catalog, base)
	case "create_checkout":
		out, err = a.ucpCreate(args.Checkout, base)
	case "get_checkout":
		out, err = a.ucpGet(args.ID, base)
	case "update_checkout":
		out, err = a.ucpUpdate(args.ID, args.Checkout, base)
	case "complete_checkout":
		out, err = a.ucpComplete(args.ID, args.Checkout, base)
	case "cancel_checkout":
		out, err = a.ucpCancel(args.ID, base)
	case "get_order":
		out, err = a.ucpOrder(args.ID, args.Buyer, args.CheckoutID, base)
	default:
		ucpJSON(w, 200, rpcError(req.ID, -32602, "unknown tool: "+p.Name))
		return
	}
	isErr := false
	var ue *ucpErr
	switch {
	case errors.As(err, &ue) && ue.Status == http.StatusTooManyRequests:
		// Rate limits are protocol errors in the UCP MCP binding (-32000).
		ucpJSON(w, 200, rpcError(req.ID, -32000, ue.Msg))
		return
	case errors.As(err, &ue):
		out, isErr = ue.body(), true
	case err != nil:
		out, isErr = (&ucpErr{Status: 500, Code: "internal_error", Msg: err.Error()}).body(), true
	}
	text, _ := json.Marshal(out)
	ucpJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
		"structuredContent": out, "content": []any{map[string]any{"type": "text", "text": string(text)}}, "isError": isErr,
	}})
}
