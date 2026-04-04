package fs

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// mockConn implements Conn for testing, backed by read/write buffers.
type mockConn struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
}

func newMockConn() *mockConn {
	return &mockConn{
		readBuf:  &bytes.Buffer{},
		writeBuf: &bytes.Buffer{},
	}
}

func (m *mockConn) Read(p []byte) (int, error)  { return m.readBuf.Read(p) }
func (m *mockConn) Write(p []byte) (int, error) { return m.writeBuf.Write(p) }

func TestReadFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	conn := newMockConn()
	params, _ := json.Marshal(PathParams{Path: path})

	result, err := Read(json.RawMessage(params), conn)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	m := result.(map[string]interface{})
	if m["size"] != 11 {
		t.Errorf("size = %v, want 11", m["size"])
	}

	// Check the binary frame was written
	frame, err := codec.ReadFrame(conn.writeBuf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame.Type != proto.FrameBinary {
		t.Errorf("frame type = %d, want binary", frame.Type)
	}
	if string(frame.Payload) != "hello world" {
		t.Errorf("payload = %q, want %q", frame.Payload, "hello world")
	}
}

func TestWriteFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sub", "out.txt")

	conn := newMockConn()
	// Write a binary frame into the read buffer
	codec.WriteFrame(conn.readBuf, proto.FrameBinary, []byte("written data"))

	params, _ := json.Marshal(WriteParams{Path: path, Size: 12})
	result, err := Write(json.RawMessage(params), conn)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	m := result.(map[string]interface{})
	if m["success"] != true {
		t.Errorf("success = %v", m["success"])
	}

	data, _ := os.ReadFile(path)
	if string(data) != "written data" {
		t.Errorf("file content = %q, want %q", data, "written data")
	}
}

func TestList(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0644)
	os.Mkdir(filepath.Join(tmp, "sub"), 0755)

	params, _ := json.Marshal(PathParams{Path: tmp})
	result, err := List(json.RawMessage(params))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	entries := result.([]FileInfo)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["a.txt"] || !names["sub"] {
		t.Errorf("entries = %v, want a.txt and sub", names)
	}
}

func TestStat(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "f.txt")
	os.WriteFile(path, []byte("abc"), 0644)

	params, _ := json.Marshal(PathParams{Path: path})
	result, err := Stat(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	fi := result.(*FileInfo)
	if fi.Name != "f.txt" {
		t.Errorf("name = %s, want f.txt", fi.Name)
	}
	if fi.Size != 3 {
		t.Errorf("size = %d, want 3", fi.Size)
	}
	if fi.IsDir {
		t.Error("is_dir = true, want false")
	}
}

func TestRemove(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "del.txt")
	os.WriteFile(path, []byte("x"), 0644)

	params, _ := json.Marshal(RemoveParams{Path: path})
	_, err := Remove(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still exists after remove")
	}
}

func TestRemoveRecursive(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "d")
	os.MkdirAll(filepath.Join(dir, "nested"), 0755)
	os.WriteFile(filepath.Join(dir, "nested", "f"), []byte("x"), 0644)

	params, _ := json.Marshal(RemoveParams{Path: dir, Recursive: true})
	_, err := Remove(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Remove recursive: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("dir still exists after recursive remove")
	}
}

func TestRename(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old.txt")
	new := filepath.Join(tmp, "new.txt")
	os.WriteFile(old, []byte("data"), 0644)

	params, _ := json.Marshal(RenameParams{Old: old, New: new})
	_, err := Rename(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	data, _ := os.ReadFile(new)
	if string(data) != "data" {
		t.Errorf("content = %q, want data", data)
	}
}

func TestMkdir(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "newdir")

	params, _ := json.Marshal(MkdirParams{Path: path})
	_, err := Mkdir(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	fi, _ := os.Stat(path)
	if !fi.IsDir() {
		t.Error("not a directory")
	}
}

func TestMkdirRecursive(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "a", "b", "c")

	params, _ := json.Marshal(MkdirParams{Path: path, Recursive: true})
	_, err := Mkdir(json.RawMessage(params))
	if err != nil {
		t.Fatalf("MkdirRecursive: %v", err)
	}

	fi, _ := os.Stat(path)
	if !fi.IsDir() {
		t.Error("not a directory")
	}
}

func TestUploadDownload(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0644)

	// Download (tar src)
	downloadConn := newMockConn()
	params, _ := json.Marshal(PathParams{Path: src})
	_, err := Download(json.RawMessage(params), downloadConn)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Read the binary frame from download
	frame, err := codec.ReadFrame(downloadConn.writeBuf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	// Upload (extract to dst)
	uploadConn := newMockConn()
	codec.WriteFrame(uploadConn.readBuf, proto.FrameBinary, frame.Payload)

	params, _ = json.Marshal(PathParams{Path: dst})
	_, err = Upload(json.RawMessage(params), uploadConn)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("content = %q, want content", data)
	}
}

func TestUploadTarCreation(t *testing.T) {
	// Create a tar in memory and verify upload extracts it
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "extracted")

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	content := []byte("tar file content")
	tw.WriteHeader(&tar.Header{
		Name: "hello.txt",
		Size: int64(len(content)),
		Mode: 0644,
	})
	tw.Write(content)
	tw.Close()

	conn := newMockConn()
	codec.WriteFrame(conn.readBuf, proto.FrameBinary, tarBuf.Bytes())

	params, _ := json.Marshal(PathParams{Path: dst})
	_, err := Upload(json.RawMessage(params), conn)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if string(data) != "tar file content" {
		t.Errorf("content = %q", data)
	}
}
