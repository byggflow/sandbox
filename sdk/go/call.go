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
