// This file is the single backbone for daemon RPC. Every category
// (FSCategory, NetCategory, ...) routes its methods through call() or
// notify() so transport handling, error wrapping, and timeout policy
// live in one place.
//
// To add a new RPC to a category:
//
//  1. Declare the operation name in github.com/byggflow/sandbox/protocol
//     (e.g. const OpFooBar = "foo.bar") so the agent and daemon share it.
//  2. On the category struct, write a method that builds the params,
//     calls call(ctx, c.cc, op{Method: protocol.OpFooBar, Params: ...}),
//     and type-asserts the result.
//  3. Server side: add a handler in agent/dispatch.go (agent-served) or
//     internal/daemon/handlers_egress.go (daemon-served) keyed by the
//     same opcode.
//
// Notification methods use notify() instead of call().
package sandbox

import (
	"context"
	"fmt"
)

// callContext holds the transport and sandbox identity for RPC calls.
type callContext struct {
	transport RpcTransport
	sandboxID string
}

// op describes a single RPC operation.
type op struct {
	Method string
	Params interface{}
}

// call performs a single RPC call through the transport.
// All SDK methods use this as the single backbone for communication.
func call(ctx context.Context, cc *callContext, o op) (interface{}, error) {
	if cc == nil {
		return nil, fmt.Errorf("sandbox: nil call context")
	}
	if cc.transport == nil {
		return nil, fmt.Errorf("sandbox: nil transport")
	}
	result, err := cc.transport.Call(ctx, o.Method, o.Params)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %s: %w", o.Method, err)
	}
	return result, nil
}

// notify sends a one-way notification through the transport.
func notify(ctx context.Context, cc *callContext, o op) error {
	if cc == nil {
		return fmt.Errorf("sandbox: nil call context")
	}
	if cc.transport == nil {
		return fmt.Errorf("sandbox: nil transport")
	}
	if err := cc.transport.Notify(ctx, o.Method, o.Params); err != nil {
		return fmt.Errorf("sandbox: %s: %w", o.Method, err)
	}
	return nil
}
