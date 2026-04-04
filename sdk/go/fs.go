package sandbox

import "context"

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
func (f *FSCategory) Read(ctx context.Context, path string) ([]byte, error) {
	result, err := call(ctx, f.cc, op{
		Method: "fs.read",
		Params: map[string]interface{}{"path": path},
	})
	if err != nil {
		return nil, err
	}
	if b, ok := result.([]byte); ok {
		return b, nil
	}
	if s, ok := result.(string); ok {
		return []byte(s), nil
	}
	return nil, &FsError{SandboxError: SandboxError{Message: "unexpected response type"}, FsCode: "EREAD"}
}

// Write writes content to the file at the given path.
func (f *FSCategory) Write(ctx context.Context, path string, content []byte) error {
	_, err := call(ctx, f.cc, op{
		Method: "fs.write",
		Params: map[string]interface{}{"path": path, "content": content},
	})
	return err
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
func (f *FSCategory) Upload(ctx context.Context, path string, tar []byte) error {
	_, err := call(ctx, f.cc, op{
		Method: "fs.upload",
		Params: map[string]interface{}{"path": path, "tar": tar},
	})
	return err
}

// Download returns the contents at the given path as a tar archive.
func (f *FSCategory) Download(ctx context.Context, path string) ([]byte, error) {
	result, err := call(ctx, f.cc, op{
		Method: "fs.download",
		Params: map[string]interface{}{"path": path},
	})
	if err != nil {
		return nil, err
	}
	if b, ok := result.([]byte); ok {
		return b, nil
	}
	if s, ok := result.(string); ok {
		return []byte(s), nil
	}
	return nil, &FsError{SandboxError: SandboxError{Message: "unexpected response type"}, FsCode: "EDOWNLOAD"}
}
