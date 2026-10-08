package sessions

import (
	"encoding/json"
	"os"
	"time"
)

// savedCacheVersion must change whenever the metadata derived from a session
// file changes. Saved entries are trusted for as long as their file is
// unchanged, so an older save would otherwise keep serving the old values.
const savedCacheVersion = 2

type savedCache struct {
	Version int
	Items   []savedCacheItem
}

type savedCacheItem struct {
	Path    string
	Session *Session
	Device  uint64
	Inode   uint64
	Size    int64
	MTime   time.Time
}

// LoadCache returns a cache that starts with the session metadata saved at
// path. A missing or unusable file is ignored: every entry can be rebuilt.
// Hidden sessions are read again after each restart, since an update may
// show them, so older saves that kept them are ignored too.
func LoadCache(path string) *Cache {
	cache := NewCache()
	cache.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	var saved savedCache
	if json.Unmarshal(data, &saved) != nil || saved.Version != savedCacheVersion {
		return cache
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, item := range saved.Items {
		if item.Session != nil {
			cache.storeSessionMetadataLocked(item.Path, item.Session, item.Device, item.Inode, item.Size, item.MTime)
		}
	}
	return cache
}

// Save writes the session metadata to the path given to LoadCache.
func (cache *Cache) Save() error {
	if cache.path == "" {
		return nil
	}
	cache.mu.Lock()
	saved := savedCache{Version: savedCacheVersion, Items: make([]savedCacheItem, 0, len(cache.metadataItems))}
	for element := cache.metadataOrder.Front(); element != nil; element = element.Next() {
		path := element.Value.(string)
		item := cache.metadataItems[path]
		if item.session == nil {
			continue
		}
		saved.Items = append(saved.Items, savedCacheItem{Path: path, Session: item.session, Device: item.device, Inode: item.inode, Size: item.size, MTime: item.mtime})
	}
	cache.mu.Unlock()
	return writeJSON(cache.path, saved)
}
