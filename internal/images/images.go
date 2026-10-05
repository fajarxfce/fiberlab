package images

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultVersion = "7.20.8"

var versionPattern = regexp.MustCompile(`^7\.[0-9]+(\.[0-9]+)?$`)

type Image struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	SHA256  string    `json:"sha256"`
	Size    int64     `json:"size"`
	Path    string    `json:"path"`
	Source  string    `json:"source"`
	AddedAt time.Time `json:"addedAt"`
}
type Manager struct {
	Dir string
	mu  sync.Mutex
}

func (m *Manager) List() ([]Image, error) {
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil, err
	}
	out := []Image{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var i Image
		if json.Unmarshal(b, &i) != nil {
			continue
		}
		digest, err := hex.DecodeString(i.SHA256)
		if !versionPattern.MatchString(i.Version) || i.ID != "chr-"+i.Version || e.Name() != i.ID+".json" || err != nil || len(digest) != sha256.Size {
			continue
		}
		i.Path = filepath.Join(m.Dir, i.ID+".img")
		if info, err := os.Stat(i.Path); err == nil && info.Mode().IsRegular() {
			out = append(out, i)
		}
	}
	return out, nil
}
func (m *Manager) Get(id string) (Image, error) {
	list, err := m.List()
	if err != nil {
		return Image{}, err
	}
	for _, i := range list {
		if i.ID == id {
			return i, nil
		}
	}
	return Image{}, fmt.Errorf("CHR image %q not found; download or import an official CHR raw image", id)
}
func (m *Manager) Fetch(ctx context.Context, version string, progress func(int64, int64)) (Image, error) {
	if version == "" {
		version = DefaultVersion
	}
	if !versionPattern.MatchString(version) {
		return Image{}, fmt.Errorf("use an exact RouterOS v7 release, for example %s", DefaultVersion)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "chr-" + version
	if i, err := m.Get(id); err == nil {
		return i, nil
	}
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return Image{}, err
	}
	url := "https://download.mikrotik.com/routeros/" + version + "/chr-" + version + ".img.zip"
	client := http.Client{Timeout: 10 * time.Minute}
	f, err := os.CreateTemp(m.Dir, ".download-*.zip")
	if err != nil {
		return Image{}, err
	}
	zipPath := f.Name()
	defer os.Remove(zipPath)
	if err = downloadChunks(ctx, &client, url, f, progress); err != nil {
		f.Close()
		return Image{}, err
	}
	if err = f.Close(); err != nil {
		return Image{}, err
	}
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		return Image{}, err
	}
	defer z.Close()
	var entry *zip.File
	for _, file := range z.File {
		if filepath.Base(file.Name) == id+".img" {
			entry = file
			break
		}
	}
	if entry == nil || entry.UncompressedSize64 > 1<<30 {
		return Image{}, fmt.Errorf("archive does not contain the expected bounded CHR image")
	}
	r, err := entry.Open()
	if err != nil {
		return Image{}, err
	}
	defer r.Close()
	return m.importReader(r, version, url)
}

// Bounded range requests survive interrupted vendor downloads without treating
// partial archives as usable images. Each completed range advances the offset.
func downloadChunks(ctx context.Context, client *http.Client, source string, f *os.File, progress func(int64, int64)) error {
	const maximum int64 = 256 << 20
	const chunk int64 = 4 << 20
	var offset int64
	total := int64(-1)
	failures := 0
	for total < 0 || offset < total {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, "GET", source, nil)
		if err != nil {
			return err
		}
		end := min(offset+chunk-1, maximum-1)
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
		response, err := client.Do(request)
		if err != nil {
			failures++
			if failures > 5 {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return fmt.Errorf("official CHR download returned HTTP %d; choose an available exact version", response.StatusCode)
		}
		if response.StatusCode == http.StatusPartialContent {
			var first, last, size int64
			if _, err := fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &size); err != nil || first != offset || last < first || size <= last {
				response.Body.Close()
				return fmt.Errorf("invalid CHR range response")
			}
			total = size
		} else {
			if offset > 0 {
				if err = f.Truncate(0); err != nil {
					response.Body.Close()
					return err
				}
				if _, err = f.Seek(0, io.SeekStart); err != nil {
					response.Body.Close()
					return err
				}
				offset = 0
			}
			total = response.ContentLength
		}
		if total > maximum {
			response.Body.Close()
			return fmt.Errorf("CHR archive exceeds 256 MiB")
		}
		buffer := make([]byte, 128<<10)
		before := offset
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if offset+int64(n) > maximum {
					response.Body.Close()
					return fmt.Errorf("CHR archive exceeds 256 MiB")
				}
				if _, err = f.Write(buffer[:n]); err != nil {
					response.Body.Close()
					return err
				}
				offset += int64(n)
				if progress != nil {
					progress(offset, total)
				}
			}
			if readErr != nil {
				err = readErr
				break
			}
		}
		response.Body.Close()
		if err == io.EOF {
			failures = 0
			if total < 0 {
				total = offset
			}
		} else {
			failures++
			if failures > 5 {
				return fmt.Errorf("CHR download interrupted at %s bytes: %w", strconv.FormatInt(offset, 10), err)
			}
		}
		if offset == before && offset != total {
			return fmt.Errorf("CHR download made no progress")
		}
	}
	return nil
}
func (m *Manager) Import(path, version string) (Image, error) {
	if !versionPattern.MatchString(version) {
		return Image{}, fmt.Errorf("an exact RouterOS v7 version is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return Image{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer f.Close()
	return m.importReader(f, version, "local import")
}
func (m *Manager) importReader(r io.Reader, version, source string) (Image, error) {
	f, err := os.CreateTemp(m.Dir, ".image-*")
	if err != nil {
		return Image{}, err
	}
	defer os.Remove(f.Name())
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(r, 1<<30+1))
	if err != nil {
		f.Close()
		return Image{}, err
	}
	if n > 1<<30 || n < 32<<20 {
		f.Close()
		return Image{}, fmt.Errorf("raw CHR image must be between 32 MiB and 1 GiB")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return Image{}, err
	}
	f.Close()
	i := Image{ID: "chr-" + version, Version: version, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n, Path: filepath.Join(m.Dir, "chr-"+version+".img"), Source: source, AddedAt: time.Now().UTC()}
	if err = os.Rename(f.Name(), i.Path); err != nil {
		return Image{}, err
	}
	b, _ := json.MarshalIndent(i, "", "  ")
	manifest := filepath.Join(m.Dir, i.ID+".json")
	if err = os.WriteFile(manifest+".tmp", b, 0600); err != nil {
		return Image{}, err
	}
	if err = os.Rename(manifest+".tmp", manifest); err != nil {
		return Image{}, err
	}
	return i, nil
}
