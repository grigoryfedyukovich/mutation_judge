package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/example/mutation-judge/internal/runner"
)

const cacheSchema = "mutation-judge-cache/v1"

type Entry struct {
	Schema string        `json:"schema"`
	Result runner.Result `json:"result"`
}

type Store struct {
	Dir     string
	Enabled bool
}

func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s Store) Get(key string) (runner.Result, bool) {
	if !s.Enabled || !validKey(key) {
		return runner.Result{}, false
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, key+".json"))
	if err != nil {
		return runner.Result{}, false
	}
	var e Entry
	if json.Unmarshal(b, &e) != nil || e.Schema != cacheSchema {
		return runner.Result{}, false
	}
	return e.Result, true
}

func (s Store) Put(key string, r runner.Result) error {
	if !s.Enabled {
		return nil
	}
	if !validKey(key) {
		return errors.New("cache key must be a 64-character lowercase hexadecimal digest")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(Entry{Schema: cacheSchema, Result: r})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, key+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.Dir, key+".json"))
}

func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
