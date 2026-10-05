package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRangeDownloadResumesAfterPartialResponse(t *testing.T) {
	content := bytes.Repeat([]byte("fiberlab-range-test"), 400000)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var first, last int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		last = min(last, len(content)-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(content)))
		w.Header().Set("Content-Length", fmt.Sprint(last-first+1))
		w.WriteHeader(http.StatusPartialContent)
		if requests.Add(1) == 1 {
			_, _ = w.Write(content[first : first+12345]) // Unexpected EOF; next request must resume here.
			return
		}
		_, _ = w.Write(content[first : last+1])
	}))
	defer server.Close()
	file, err := os.CreateTemp(t.TempDir(), "download")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = downloadChunks(context.Background(), server.Client(), server.URL, file, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file.Name())
	if !bytes.Equal(got, content) || requests.Load() < 3 {
		t.Fatal("range resume corrupted the image")
	}
}

func TestDownloadRejectsOversizedArchiveBeforeWriting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-10/268435457")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 11))
	}))
	defer server.Close()
	file, err := os.CreateTemp(t.TempDir(), "download")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if downloadChunks(context.Background(), server.Client(), server.URL, file, nil) == nil {
		t.Fatal("oversized image accepted")
	}
	info, _ := file.Stat()
	if info.Size() != 0 {
		t.Fatal("oversized archive wrote bytes")
	}
}

func TestRawImportAndDigestCatalog(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	data := bytes.Repeat([]byte{0x71}, 32<<20)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Dir: filepath.Join(dir, "images")}
	image, err := manager.Import(source, "7.20.8")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if image.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("wrong imported SHA")
	}
	list, err := manager.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("image catalog: %+v %v", list, err)
	}
	// A local malformed catalog cannot turn an image ID into path traversal.
	image.ID = "../../source"
	b, _ := json.Marshal(image)
	os.WriteFile(filepath.Join(manager.Dir, "evil.json"), b, 0600)
	list, err = manager.List()
	if err != nil || len(list) != 1 {
		t.Fatal("malformed catalog record admitted")
	}
}
