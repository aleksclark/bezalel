package server

import (
	"context"
	"encoding/json"
	"fmt"
)

// toolFunc is the uniform shape every tool implementation satisfies.
type toolFunc[P any] func(context.Context, P) (string, error)

// toolHandler unmarshals raw JSON arguments and invokes a tool, returning an
// MCP tool result (or a JSON-RPC error for malformed params).
type toolHandler func(ctx context.Context, args json.RawMessage) (any, *jsonrpcError)

// toolDef is a single registered tool: its MCP metadata plus its handler.
type toolDef struct {
	name        string
	description string
	schema      map[string]any
	handler     toolHandler
}

// registry holds tools in registration order, indexed by name for dispatch.
type registry struct {
	order  []*toolDef
	byName map[string]*toolDef
}

func newRegistry() *registry {
	return &registry{byName: map[string]*toolDef{}}
}

// add registers a tool. bind() produces the handler from a uniform tool func.
func (r *registry) add(name, description string, schema map[string]any, h toolHandler) {
	d := &toolDef{name: name, description: description, schema: schema, handler: h}
	r.order = append(r.order, d)
	r.byName[name] = d
}

// list returns the MCP tools/list payload in registration order.
func (r *registry) list() any {
	defs := make([]map[string]any, 0, len(r.order))
	for _, d := range r.order {
		defs = append(defs, map[string]any{
			"name":        d.name,
			"description": d.description,
			"inputSchema": d.schema,
		})
	}
	return map[string]any{"tools": defs}
}

// call dispatches a tools/call to the named tool.
func (r *registry) call(ctx context.Context, name string, args json.RawMessage) (any, *jsonrpcError) {
	d, ok := r.byName[name]
	if !ok {
		return toolResult(fmt.Sprintf("Unknown tool: %s", name), true), nil
	}
	return d.handler(ctx, args)
}

// bind adapts a uniform tool function into a toolHandler, centralizing argument
// unmarshalling and the tool-error → result convention so individual tools need
// no transport boilerplate.
func bind[P any](fn toolFunc[P]) toolHandler {
	return func(ctx context.Context, args json.RawMessage) (any, *jsonrpcError) {
		var p P
		if len(args) > 0 {
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
			}
		}
		out, err := fn(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(out, false), nil
	}
}

// toolResult builds an MCP tools/call result with a single text content block.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// --- JSON-Schema builders -------------------------------------------------

// object builds an object input schema from a property map and required keys.
func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// prop builds a scalar schema property.
func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

// enumProp builds a string property constrained to a set of values.
func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

// arrayProp builds an array property whose items match the given schema.
func arrayProp(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}
