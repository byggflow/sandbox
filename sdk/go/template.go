package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SaveOptions configures template saving.
type SaveOptions struct {
	Label string `json:"label,omitempty"`
}

// TemplateInfo describes a saved template.
type TemplateInfo struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Image string `json:"image,omitempty"`
}

// TemplateCategory provides template operations on a sandbox instance.
type TemplateCategory struct {
	cc *callContext
}

// Save commits the current sandbox state as a reusable template.
func (t *TemplateCategory) Save(ctx context.Context, opts *SaveOptions) (string, error) {
	params := map[string]interface{}{}
	if opts != nil && opts.Label != "" {
		params["label"] = opts.Label
	}
	result, err := call(ctx, t.cc, op{
		Method: "template.save",
		Params: params,
	})
	if err != nil {
		return "", err
	}
	if m, ok := result.(map[string]interface{}); ok {
		if id, ok := m["id"].(string); ok {
			return id, nil
		}
	}
	return "", &SandboxError{Message: "unexpected response type for template.save"}
}

// TemplateManager provides standalone template management operations.
// This is returned by the top-level Templates() function.
type TemplateManager struct {
	endpoint string
	auth     Auth
}

// List returns all available templates.
func (tm *TemplateManager) List(ctx context.Context) ([]TemplateInfo, error) {
	client, baseURL := httpClientForEndpoint(tm.endpoint)
	headers, err := resolveAuthHeaders(ctx, tm.auth, http.MethodGet, "/templates")
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/templates", nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("template list: %v", err),
		}}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: template list (status %d): %s", resp.StatusCode, string(body))
	}

	var templates []TemplateInfo
	if err := json.NewDecoder(resp.Body).Decode(&templates); err != nil {
		return nil, fmt.Errorf("sandbox: decode template list: %w", err)
	}
	return templates, nil
}

// Get returns information about a specific template.
func (tm *TemplateManager) Get(ctx context.Context, id string) (*TemplateInfo, error) {
	if id == "" {
		return nil, &SandboxError{Message: "template id required"}
	}

	client, baseURL := httpClientForEndpoint(tm.endpoint)
	headers, err := resolveAuthHeaders(ctx, tm.auth, http.MethodGet, "/templates/"+id)
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/templates/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("template get: %v", err),
		}}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: template get (status %d): %s", resp.StatusCode, string(body))
	}

	var info TemplateInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("sandbox: decode template: %w", err)
	}
	return &info, nil
}

// Delete removes a template.
func (tm *TemplateManager) Delete(ctx context.Context, id string) error {
	if id == "" {
		return &SandboxError{Message: "template id required"}
	}

	client, baseURL := httpClientForEndpoint(tm.endpoint)
	headers, err := resolveAuthHeaders(ctx, tm.auth, http.MethodDelete, "/templates/"+id)
	if err != nil {
		return fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/templates/"+id, nil)
	if err != nil {
		return fmt.Errorf("sandbox: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("template delete: %v", err),
		}}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sandbox: template delete (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// Templates returns a manager for standalone template operations.
func Templates(endpoint string, auth Auth) *TemplateManager {
	return &TemplateManager{
		endpoint: resolveEndpoint(endpoint),
		auth:     auth,
	}
}
