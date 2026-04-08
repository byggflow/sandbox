package fs

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// allowedPrefixes are the directories that filesystem operations are restricted to.
// Paths outside these prefixes are rejected to prevent information disclosure
// (e.g. reading /etc/shadow, /proc/*/environ) even though the container has
// read-only rootfs and dropped capabilities.
var allowedPrefixes = []string{"/root", "/tmp"}

func init() {
	if v := os.Getenv("SANDBOX_FS_ALLOWED"); v != "" {
		allowedPrefixes = strings.Split(v, ":")
	}
}

// safePath resolves a path and checks it falls within an allowed prefix.
// Returns the cleaned absolute path or an error.
func safePath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}

	// Evaluate symlinks and resolve to absolute path.
	// Use filepath.Clean first in case the path doesn't exist yet.
	cleaned := filepath.Clean(raw)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join("/root", cleaned)
	}

	// Try to resolve symlinks for the longest existing prefix.
	resolved, err := resolveExisting(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	for _, prefix := range allowedPrefixes {
		// Resolve the prefix itself so symlinks in the prefix (e.g. /tmp -> /private/tmp)
		// are handled correctly.
		resolvedPrefix, err := resolveExisting(prefix)
		if err != nil {
			resolvedPrefix = filepath.Clean(prefix)
		}
		if resolved == resolvedPrefix || strings.HasPrefix(resolved, resolvedPrefix+"/") {
			return resolved, nil
		}
		// Also check the raw prefix for cases where the path doesn't exist on disk.
		cleanPrefix := filepath.Clean(prefix)
		if resolved == cleanPrefix || strings.HasPrefix(resolved, cleanPrefix+"/") {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("access denied: path %q is outside allowed directories", raw)
}

// resolveExisting resolves symlinks for the longest existing ancestor of path,
// then appends the remaining unresolved suffix. This handles the case where the
// target file doesn't exist yet (e.g. fs.write to a new file).
func resolveExisting(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}

	// Walk up until we find a path that exists.
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if dir == path {
		// Reached filesystem root without finding an existing path.
		return path, nil
	}

	resolvedDir, err := resolveExisting(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedDir, base), nil
}

// Conn is an alias for io.ReadWriter used by handlers that need binary frame I/O.
type Conn = io.ReadWriter

// PathParams is shared params with a path field.
type PathParams struct {
	Path string `json:"path"`
}

// WriteParams is the params for fs.write.
type WriteParams struct {
	Path    string `json:"path"`
	Size    int    `json:"size"`
	Chunked bool   `json:"chunked,omitempty"`
	Chunks  int    `json:"chunks,omitempty"`
}

// RemoveParams is the params for fs.remove.
type RemoveParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// RenameParams is the params for fs.rename.
type RenameParams struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// MkdirParams is the params for fs.mkdir.
type MkdirParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// FileInfo is a serializable file info entry.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

func fileInfoFrom(fi os.FileInfo) FileInfo {
	return FileInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		IsDir:   fi.IsDir(),
		Mode:    uint32(fi.Mode()),
		ModTime: fi.ModTime(),
	}
}

// Read reads a file and sends its content as binary frame(s) followed by a JSON result.
// Files larger than the chunk threshold are sent as multiple binary frames.
func Read(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	size := len(data)
	chunkSize := proto.ChunkSize
	chunked := size > proto.ChunkThreshold
	chunks := 1

	if chunked {
		chunks = (size + chunkSize - 1) / chunkSize
		for i := 0; i < chunks; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if end > size {
				end = size
			}
			if err := codec.WriteBinary(conn, data[start:end]); err != nil {
				return nil, fmt.Errorf("write binary chunk %d: %w", i, err)
			}
		}
	} else {
		if err := codec.WriteBinary(conn, data); err != nil {
			return nil, fmt.Errorf("write binary frame: %w", err)
		}
	}

	return map[string]interface{}{
		"size":    size,
		"chunked": chunked,
		"chunks":  chunks,
	}, nil
}

// Write reads binary frame(s) from conn and writes them to the file at path.
// If chunked is true, it reads the specified number of chunk frames.
func Write(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p WriteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create parent dirs: %w", err)
	}

	if p.Chunked && p.Chunks > 1 {
		// Read multiple binary frames and concatenate.
		data := make([]byte, 0, p.Size)
		for i := 0; i < p.Chunks; i++ {
			frame, err := codec.ReadFrame(conn)
			if err != nil {
				return nil, fmt.Errorf("read binary chunk %d: %w", i, err)
			}
			data = append(data, frame.Payload...)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	} else {
		// Single binary frame (original behavior).
		frame, err := codec.ReadFrame(conn)
		if err != nil {
			return nil, fmt.Errorf("read binary frame: %w", err)
		}
		if err := os.WriteFile(path, frame.Payload, 0644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
	}

	return map[string]interface{}{"success": true}, nil
}

// List returns directory entries.
func List(raw json.RawMessage) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	result := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, fileInfoFrom(info))
	}

	return result, nil
}

// Stat returns file info for a path.
func Stat(raw json.RawMessage) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	fi := fileInfoFrom(info)
	return &fi, nil
}

// Remove removes a file or directory.
func Remove(raw json.RawMessage) (interface{}, error) {
	var p RemoveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	if p.Recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return nil, fmt.Errorf("remove: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// Rename renames a file or directory.
func Rename(raw json.RawMessage) (interface{}, error) {
	var p RenameParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	oldPath, err := safePath(p.Old)
	if err != nil {
		return nil, err
	}
	newPath, err := safePath(p.New)
	if err != nil {
		return nil, err
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// Mkdir creates a directory.
func Mkdir(raw json.RawMessage) (interface{}, error) {
	var p MkdirParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	if p.Recursive {
		err = os.MkdirAll(path, 0755)
	} else {
		err = os.Mkdir(path, 0755)
	}
	if err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// Upload reads a binary frame containing tar data and extracts it to the given path.
func Upload(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	frame, err := codec.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("read binary frame: %w", err)
	}

	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create target dir: %w", err)
	}

	tr := tar.NewReader(bytes.NewReader(frame.Payload))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		// Reject symlink entries to prevent symlink-based escape attacks.
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("tar entry %q: symlinks and hard links are not allowed", hdr.Name)
		}

		target := filepath.Join(path, hdr.Name)

		// Prevent path traversal: resolved path must stay within the base directory.
		// Use safePath to resolve symlinks on disk, not just lexical checks,
		// so that pre-existing symlinks cannot be used to escape the sandbox.
		resolvedTarget, err := safePath(target)
		if err != nil {
			return nil, fmt.Errorf("tar entry %q: %w", hdr.Name, err)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(resolvedTarget, os.FileMode(hdr.Mode)); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", resolvedTarget, err)
			}
		case tar.TypeReg:
			dir := filepath.Dir(resolvedTarget)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("mkdir parent %s: %w", dir, err)
			}
			f, err := os.OpenFile(resolvedTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return nil, fmt.Errorf("create file %s: %w", resolvedTarget, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return nil, fmt.Errorf("write file %s: %w", resolvedTarget, err)
			}
			f.Close()
		}
	}

	return map[string]interface{}{"success": true}, nil
}

// Download creates a tar archive of the given path and sends it as a binary frame.
func Download(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	path, err := safePath(p.Path)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	if !info.IsDir() {
		// Single file
		if err := addFileToTar(tw, path, info.Name()); err != nil {
			return nil, err
		}
	} else {
		base := path
		err = filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}

			if fi.IsDir() {
				hdr := &tar.Header{
					Name:     rel + "/",
					Mode:     int64(fi.Mode()),
					Typeflag: tar.TypeDir,
					ModTime:  fi.ModTime(),
				}
				return tw.WriteHeader(hdr)
			}

			return addFileToTar(tw, path, rel)
		})
		if err != nil {
			return nil, fmt.Errorf("walk: %w", err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}

	if err := codec.WriteBinary(conn, buf.Bytes()); err != nil {
		return nil, fmt.Errorf("write binary frame: %w", err)
	}

	return map[string]interface{}{"size": buf.Len()}, nil
}

func addFileToTar(tw *tar.Writer, path, name string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	hdr := &tar.Header{
		Name:     name,
		Size:     fi.Size(),
		Mode:     int64(fi.Mode()),
		Typeflag: tar.TypeReg,
		ModTime:  fi.ModTime(),
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write header %s: %w", name, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s: %w", name, err)
	}

	return nil
}
