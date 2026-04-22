package bridge

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// mediaCache is an LRU disk cache. Eviction is enforced on insert when
// the total size exceeds maxBytes.
type mediaCache struct {
	dir      string
	maxBytes int64

	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	total   int64
}

type mcEntry struct {
	key      string // filename without extension
	filename string
	size     int64
	mime     string
}

func newMediaCache(dir string, maxBytes int64) (*mediaCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &mediaCache{
		dir:      dir,
		maxBytes: maxBytes,
		entries:  map[string]*list.Element{},
		lru:      list.New(),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ext := filepath.Ext(name)
		key := strings.TrimSuffix(name, ext)
		entry := &mcEntry{
			key:      key,
			filename: name,
			size:     info.Size(),
			mime:     mimeFromExt(ext),
		}
		el := c.lru.PushFront(entry)
		c.entries[key] = el
		c.total += info.Size()
	}
	for c.total > c.maxBytes {
		if !c.evictLocked() {
			break
		}
	}
	return c, nil
}

// Get returns the absolute file path and mime type, or ok=false.
func (c *mediaCache) Get(mediaID string) (string, string, bool) {
	key := cacheKey(mediaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return "", "", false
	}
	c.lru.MoveToFront(el)
	e := el.Value.(*mcEntry)
	return filepath.Join(c.dir, e.filename), e.mime, true
}

// Put writes via fn to a temp file, then installs it atomically as the
// cache entry for mediaID. Returns the final path on success.
func (c *mediaCache) Put(mediaID, mime string, fn func(w io.Writer) error) (string, error) {
	key := cacheKey(mediaID)
	ext := extFromMime(mime)
	fname := key + ext
	tmpPath := filepath.Join(c.dir, fname+".tmp")
	finalPath := filepath.Join(c.dir, fname)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if err := fn(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		old := el.Value.(*mcEntry)
		c.total -= old.size
		c.lru.Remove(el)
	}
	entry := &mcEntry{key: key, filename: fname, size: info.Size(), mime: mime}
	el := c.lru.PushFront(entry)
	c.entries[key] = el
	c.total += info.Size()

	for c.total > c.maxBytes && c.lru.Len() > 1 {
		c.evictLocked()
	}
	return finalPath, nil
}

// evictLocked removes the least-recently-used entry. Caller holds mu.
func (c *mediaCache) evictLocked() bool {
	el := c.lru.Back()
	if el == nil {
		return false
	}
	c.lru.Remove(el)
	e := el.Value.(*mcEntry)
	delete(c.entries, e.key)
	c.total -= e.size
	_ = os.Remove(filepath.Join(c.dir, e.filename))
	return true
}

func cacheKey(mediaID string) string {
	sum := sha256.Sum256([]byte(mediaID))
	return hex.EncodeToString(sum[:10])
}

func mimeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

func extFromMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".bin"
}
