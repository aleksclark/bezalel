package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func resultText(t *testing.T, res any) (string, bool) {
	t.Helper()
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result is not a map: %T", res)
	}
	isErr, _ := m["isError"].(bool)
	content, ok := m["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %v", m)
	}
	return content[0]["text"].(string), isErr
}

func TestBindSuccess(t *testing.T) {
	h := bind(func(_ context.Context, p struct {
		Name string `json:"name"`
	}) (string, error) {
		return "hello " + p.Name, nil
	})

	res, rpcErr := h(context.Background(), json.RawMessage(`{"name":"world"}`))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	text, isErr := resultText(t, res)
	if isErr || text != "hello world" {
		t.Errorf("got (%q, %v), want (hello world, false)", text, isErr)
	}
}

func TestBindToolErrorBecomesResult(t *testing.T) {
	h := bind(func(_ context.Context, _ struct{}) (string, error) {
		return "", errors.New("boom")
	})

	res, rpcErr := h(context.Background(), json.RawMessage(`{}`))
	if rpcErr != nil {
		t.Fatalf("tool errors must not surface as JSON-RPC errors: %v", rpcErr)
	}
	text, isErr := resultText(t, res)
	if !isErr || text != "boom" {
		t.Errorf("got (%q, %v), want (boom, true)", text, isErr)
	}
}

func TestBindMalformedArgs(t *testing.T) {
	h := bind(func(_ context.Context, _ struct {
		N int `json:"n"`
	}) (string, error) {
		return "ok", nil
	})

	_, rpcErr := h(context.Background(), json.RawMessage(`{"n": "not-an-int"}`))
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("expected -32602 invalid-args error, got %v", rpcErr)
	}
}

func TestBindEmptyArgsUsesZeroValue(t *testing.T) {
	called := false
	h := bind(func(_ context.Context, _ struct{}) (string, error) {
		called = true
		return "ran", nil
	})
	if _, rpcErr := h(context.Background(), nil); rpcErr != nil {
		t.Fatalf("nil args should be allowed, got %v", rpcErr)
	}
	if !called {
		t.Error("handler was not invoked for nil args")
	}
}

func TestRegistryListPreservesOrder(t *testing.T) {
	r := newRegistry()
	noop := bind(func(_ context.Context, _ struct{}) (string, error) { return "", nil })
	r.add("alpha", "first", object(nil), noop)
	r.add("beta", "second", object(nil), noop)

	payload := r.list().(map[string]any)
	defs := payload["tools"].([]map[string]any)
	if len(defs) != 2 || defs[0]["name"] != "alpha" || defs[1]["name"] != "beta" {
		t.Fatalf("registration order not preserved: %v", defs)
	}
	if defs[0]["description"] != "first" {
		t.Errorf("description mismatch: %v", defs[0])
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	r := newRegistry()
	res, rpcErr := r.call(context.Background(), "nope", nil)
	if rpcErr != nil {
		t.Fatalf("unknown tool should be a tool result, not rpc error: %v", rpcErr)
	}
	text, isErr := resultText(t, res)
	if !isErr || text == "" {
		t.Errorf("expected isError result for unknown tool, got (%q, %v)", text, isErr)
	}
}

func TestObjectSchemaRequired(t *testing.T) {
	s := object(map[string]any{"x": prop("string", "an x")}, "x")
	if s["type"] != "object" {
		t.Errorf("type = %v, want object", s["type"])
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "x" {
		t.Errorf("required = %v, want [x]", s["required"])
	}

	// No required keys -> the field is omitted entirely.
	if _, present := object(nil)["required"]; present {
		t.Error("empty object schema should omit 'required'")
	}
}

func TestHandleInitializeEchoesProtocol(t *testing.T) {
	s := &Server{}
	out := s.handleInitialize(json.RawMessage(`{"protocolVersion":"2030-01-01"}`)).(map[string]any)
	if out["protocolVersion"] != "2030-01-01" {
		t.Errorf("protocolVersion = %v, want echoed client value", out["protocolVersion"])
	}

	out = s.handleInitialize(nil).(map[string]any)
	if out["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion = %v, want default %s", out["protocolVersion"], mcpProtocolVersion)
	}
}
