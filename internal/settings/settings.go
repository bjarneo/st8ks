// Package settings stores user preferences in the platform config directory.
package settings

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// SavedView is a named list filter for one resource kind.
type SavedView struct {
	Kind  string `json:"kind"`
	Query string `json:"query"`
}

// Settings holds every persisted preference.
type Settings struct {
	Theme            string              `json:"theme"`
	Density          string              `json:"density"`
	DetailLayout     string              `json:"detailLayout"`
	OverviewStyle    string              `json:"overviewStyle"`
	Collapsed        map[string]bool     `json:"collapsed"`
	SavedViews       []SavedView         `json:"savedViews"`
	Kubeconfigs      []string            `json:"kubeconfigs"`
	ScanKubeDir      bool                `json:"scanKubeDir"`
	LastContext      string              `json:"lastContext"`
	NsByContext      map[string][]string `json:"nsByContext"`
	ProtectedPattern string              `json:"protectedPattern"`
	AnthropicKey     string              `json:"anthropicKey,omitempty"`
	AssistantModel   string              `json:"assistantModel"`
	LogTail          int                 `json:"logTail"`
}

// Defaults returns the settings for a first start.
func Defaults() Settings {
	return Settings{
		Theme:            "dark",
		Density:          "compact",
		DetailLayout:     "drawer",
		OverviewStyle:    "metrics",
		Collapsed:        map[string]bool{"config": true, "storage": true, "crd": true},
		SavedViews:       []SavedView{},
		Kubeconfigs:      []string{},
		ScanKubeDir:      true,
		NsByContext:      map[string][]string{},
		ProtectedPattern: `(^|[-_.])(prod|production|prd|live)($|[-_.])`,
		AssistantModel:   "claude-opus-5",
		LogTail:          5000,
	}
}

// Store loads and saves settings. It is safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	wmu  sync.Mutex // orders the file writes
	path string
	s    Settings
}

// Open reads the settings file. A missing or broken file gives the defaults.
func Open() *Store {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	st := &Store{path: filepath.Join(dir, "st8ks", "settings.json"), s: Defaults()}
	if b, err := os.ReadFile(st.path); err == nil {
		s := Defaults()
		if json.Unmarshal(b, &s) == nil {
			st.s = normalize(s)
		}
	}
	return st
}

func normalize(s Settings) Settings {
	d := Defaults()
	if s.Theme == "" {
		s.Theme = d.Theme
	}
	if s.Density == "" {
		s.Density = d.Density
	}
	if s.DetailLayout == "" {
		s.DetailLayout = d.DetailLayout
	}
	if s.OverviewStyle == "" {
		s.OverviewStyle = d.OverviewStyle
	}
	if s.Collapsed == nil {
		s.Collapsed = d.Collapsed
	}
	if s.SavedViews == nil {
		s.SavedViews = []SavedView{}
	}
	if s.Kubeconfigs == nil {
		s.Kubeconfigs = []string{}
	}
	if s.NsByContext == nil {
		s.NsByContext = map[string][]string{}
	}
	if s.ProtectedPattern == "" {
		s.ProtectedPattern = d.ProtectedPattern
	}
	if s.AssistantModel == "" {
		s.AssistantModel = d.AssistantModel
	}
	if s.LogTail <= 0 {
		s.LogTail = d.LogTail
	}
	return s
}

// Get returns a deep copy of the current settings, so callers can change
// it without a race.
func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return clone(st.s)
}

func clone(s Settings) Settings {
	s.Collapsed = maps.Clone(s.Collapsed)
	s.SavedViews = slices.Clone(s.SavedViews)
	s.Kubeconfigs = slices.Clone(s.Kubeconfigs)
	ns := make(map[string][]string, len(s.NsByContext))
	for k, v := range s.NsByContext {
		ns[k] = slices.Clone(v)
	}
	s.NsByContext = ns
	return s
}

// Update changes the settings with fn and writes the file.
func (st *Store) Update(fn func(s *Settings)) error {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	st.mu.Lock()
	next := clone(st.s)
	fn(&next)
	st.s = normalize(next)
	b, err := json.MarshalIndent(st.s, "", "  ")
	st.mu.Unlock()
	if err != nil {
		return err
	}
	return writeAtomic(st.path, b)
}

// writeAtomic writes to a temporary file and renames it. The file can hold an
// API key, so only the owner can read it.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
