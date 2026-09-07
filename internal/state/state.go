package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type File struct {
	mu        sync.RWMutex
	Path      string
	Selectors map[string]string
	root      map[string]json.RawMessage
}

func Load(path string) *File {
	f := &File{Path: path, Selectors: map[string]string{}, root: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) == nil && root != nil {
		f.root = root
		var rawSelectors map[string]json.RawMessage
		if json.Unmarshal(root["selectors"], &rawSelectors) == nil {
			for name, rawValue := range rawSelectors {
				var value string
				if json.Unmarshal(rawValue, &value) == nil {
					f.Selectors[name] = value
				}
			}
		}
	}
	return f
}

func (f *File) GetSelector(name string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	value, ok := f.Selectors[name]
	return value, ok
}

func (f *File) SyncSelectors(values map[string]string, scope []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Selectors == nil {
		f.Selectors = map[string]string{}
	}
	if f.root == nil {
		f.root = map[string]json.RawMessage{}
	}
	if len(values) == 0 && len(scope) == 0 {
		return nil
	}
	for _, name := range scope {
		if _, ok := values[name]; !ok {
			delete(f.Selectors, name)
		}
	}
	for name, value := range values {
		f.Selectors[name] = value
	}
	return f.saveLocked()
}

func (f *File) Save() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveLocked()
}

func (f *File) saveLocked() error {
	if f.Path == "" {
		return errors.New("state path is empty")
	}
	selectors := make(map[string]string, len(f.Selectors))
	for name, value := range f.Selectors {
		selectors[name] = value
	}
	root := make(map[string]json.RawMessage, len(f.root)+1)
	for name, value := range f.root {
		root[name] = append(json.RawMessage(nil), value...)
	}
	selectorJSON, err := json.Marshal(selectors)
	if err != nil {
		return err
	}
	root["selectors"] = selectorJSON
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(f.Path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".sing-box-drover-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmpName, f.Path)
}
