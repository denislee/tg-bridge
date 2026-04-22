package bridge

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

const mediaKeySize = 32

// loadOrCreateMediaKey returns a stable 32-byte secret used to HMAC media
// IDs. The key is persisted under sessionDir/media.key.
func loadOrCreateMediaKey(sessionDir string) ([]byte, error) {
	path := filepath.Join(sessionDir, "media.key")
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != mediaKeySize {
			return nil, fmt.Errorf("media key at %s has wrong size %d", path, len(data))
		}
		return data, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, mediaKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
