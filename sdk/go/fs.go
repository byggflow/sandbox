package sandbox

import (
	"bytes"
	"context"
	"fmt"
)

// FileInfo holds metadata about a file or directory.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    int    `json:"mode"`
	IsDir   bool   `json:"isDir"`
	ModTime int64  `json:"modTime"`
}

// FSCategory provides filesystem operations on a sandbox.
type FSCategory struct {
	cc *callContext
}

// Read returns the contents of the file at the given path.
// The agent sends binary frame(s) with file content followed by a JSON response.
func (f *FSCategory) Read(ctx context.Context, path string) ([]byte, error) {
	result, bufs, err := f.cc.transport.CallExpectBinary(ctx, "fs.read", map[string]interface{}{"path": path})
	if err != nil {
		return nil, fmt.Errorf("sandbox: fs.read: %w", err)
	}
	if len(bufs) == 0 {
		return nil, &FsError{SandboxError: SandboxError{Message: "no binary data received"}, FsCode: "EREAD"}
	}
	if len(bufs) == 1 {
		return bufs[0], nil
	}
	// Reassemble chunked response.
	_ = result // result contains {size, chunked, chunks} metadata
	var buf bytes.Buffer
	for _, b := range bufs {
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

// Write writes content to the file at the given path.
// Sends the JSON-RPC call followed by binary frame(s) with the file content,
// matching the protocol the agent expects.
func (f *FSCategory) Write(ctx context.Context, path string, content []byte) error {
	chunkSize := 1024 * 1024 // 1MB
	chunked := len(content) > chunkSize
	chunks := 1
	if chunked {
		chunks = (len(content) + chunkSize - 1) / chunkSize
	}

	params := map[string]interface{}{
		"path": path,
		"size": len(content),
	}
	if chunked {
		params["chunked"] = true
		params["chunks"] = chunks
	}

	_, err := f.cc.transport.CallWithBinary(ctx, "fs.write", params, content)
	if err != nil {
		return fmt.Errorf("sandbox: fs.write: %w", err)
	}
	return nil
}

// List returns the entries in the directory at the given path.
func (f *FSCategory) List(ctx context.Context, path string) ([]string, error) {
	result, err := call(ctx, f.cc, op{
		Method: "fs.list",
		Params: map[string]interface{}{"path": path},
	})
	if err != nil {
		return nil, err
	}
	if items, ok := result.([]interface{}); ok {
		names := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
		return names, nil
	}
	return nil, &FsError{SandboxError: SandboxError{Message: "unexpected response type"}, FsCode: "ELIST"}
}

// Stat returns metadata about the file or directory at the given path.
func (f *FSCategory) Stat(ctx context.Context, path string) (*FileInfo, error) {
	result, err := call(ctx, f.cc, op{
		Method: "fs.stat",
		Params: map[string]interface{}{"path": path},
	})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		info := &FileInfo{}
		if v, ok := m["name"].(string); ok {
			info.Name = v
		}
		if v, ok := m["size"].(float64); ok {
			info.Size = int64(v)
		}
		if v, ok := m["mode"].(float64); ok {
			info.Mode = int(v)
		}
		if v, ok := m["isDir"].(bool); ok {
			info.IsDir = v
		}
		if v, ok := m["modTime"].(float64); ok {
			info.ModTime = int64(v)
		}
		return info, nil
	}
	return nil, &FsError{SandboxError: SandboxError{Message: "unexpected response type"}, FsCode: "ESTAT"}
}

// Remove deletes the file or directory at the given path.
func (f *FSCategory) Remove(ctx context.Context, path string) error {
	_, err := call(ctx, f.cc, op{
		Method: "fs.remove",
		Params: map[string]interface{}{"path": path},
	})
	return err
}

// Rename moves a file or directory from oldPath to newPath.
func (f *FSCategory) Rename(ctx context.Context, oldPath, newPath string) error {
	_, err := call(ctx, f.cc, op{
		Method: "fs.rename",
		Params: map[string]interface{}{"oldPath": oldPath, "newPath": newPath},
	})
	return err
}

// Mkdir creates a directory at the given path.
func (f *FSCategory) Mkdir(ctx context.Context, path string) error {
	_, err := call(ctx, f.cc, op{
		Method: "fs.mkdir",
		Params: map[string]interface{}{"path": path},
	})
	return err
}

// Upload sends a tar archive to be extracted at the given path.
// Sends the JSON-RPC call followed by a binary frame with the tar data.
func (f *FSCategory) Upload(ctx context.Context, path string, tar []byte) error {
	params := map[string]interface{}{
		"path": path,
		"size": len(tar),
	}
	_, err := f.cc.transport.CallWithBinary(ctx, "fs.upload", params, tar)
	if err != nil {
		return fmt.Errorf("sandbox: fs.upload: %w", err)
	}
	return nil
}

// Download returns the contents at the given path as a tar archive.
// The agent sends a binary frame with the tar data followed by a JSON response.
func (f *FSCategory) Download(ctx context.Context, path string) ([]byte, error) {
	_, bufs, err := f.cc.transport.CallExpectBinary(ctx, "fs.download", map[string]interface{}{"path": path})
	if err != nil {
		return nil, fmt.Errorf("sandbox: fs.download: %w", err)
	}
	if len(bufs) == 0 {
		return nil, &FsError{SandboxError: SandboxError{Message: "no binary data received"}, FsCode: "EDOWNLOAD"}
	}
	if len(bufs) == 1 {
		return bufs[0], nil
	}
	var buf bytes.Buffer
	for _, b := range bufs {
		buf.Write(b)
	}
	return buf.Bytes(), nil
}
