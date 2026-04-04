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
)

// Conn is an alias for io.ReadWriter used by handlers that need binary frame I/O.
type Conn = io.ReadWriter

// PathParams is shared params with a path field.
type PathParams struct {
	Path string `json:"path"`
}

// WriteParams is the params for fs.write.
type WriteParams struct {
	Path string `json:"path"`
	Size int    `json:"size"`
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

// Read reads a file and sends its content as a binary frame followed by a JSON result.
func Read(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if err := codec.WriteBinary(conn, data); err != nil {
		return nil, fmt.Errorf("write binary frame: %w", err)
	}

	return map[string]interface{}{"size": len(data)}, nil
}

// Write reads a binary frame from conn and writes it to the file at path.
func Write(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p WriteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	frame, err := codec.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("read binary frame: %w", err)
	}

	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create parent dirs: %w", err)
	}

	if err := os.WriteFile(p.Path, frame.Payload, 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// List returns directory entries.
func List(raw json.RawMessage) (interface{}, error) {
	var p PathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	entries, err := os.ReadDir(p.Path)
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
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	info, err := os.Stat(p.Path)
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
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	var err error
	if p.Recursive {
		err = os.RemoveAll(p.Path)
	} else {
		err = os.Remove(p.Path)
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
	if p.Old == "" || p.New == "" {
		return nil, fmt.Errorf("old and new are required")
	}

	if err := os.Rename(p.Old, p.New); err != nil {
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
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	var err error
	if p.Recursive {
		err = os.MkdirAll(p.Path, 0755)
	} else {
		err = os.Mkdir(p.Path, 0755)
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
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	frame, err := codec.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("read binary frame: %w", err)
	}

	if err := os.MkdirAll(p.Path, 0755); err != nil {
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

		target := filepath.Join(p.Path, hdr.Name)

		// Prevent path traversal: resolved path must stay within the base directory.
		cleanBase := filepath.Clean(p.Path) + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), cleanBase) && filepath.Clean(target) != filepath.Clean(p.Path) {
			return nil, fmt.Errorf("tar entry %q escapes target directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("mkdir parent %s: %w", dir, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return nil, fmt.Errorf("create file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return nil, fmt.Errorf("write file %s: %w", target, err)
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
	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	info, err := os.Stat(p.Path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	if !info.IsDir() {
		// Single file
		if err := addFileToTar(tw, p.Path, info.Name()); err != nil {
			return nil, err
		}
	} else {
		base := p.Path
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
