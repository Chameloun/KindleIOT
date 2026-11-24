package checklist

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kindleiot/internal/config"
)

// Store manages persistent interactive checklists backed by a JSON file on disk.
type Store struct {
	mu       sync.RWMutex
	path     string
	items    map[string][]config.ChecklistItem
	onChange func()
}

// NewStore opens (or initializes) a checklist store at path.
func NewStore(path string, onChange func()) *Store {
	s := &Store{
		path:     path,
		items:    make(map[string][]config.ChecklistItem),
		onChange: onChange,
	}
	s.load()
	return s
}

func (s *Store) load() {
	if strings.TrimSpace(s.path) == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	var loaded map[string][]config.ChecklistItem
	if err := json.Unmarshal(data, &loaded); err == nil && loaded != nil {
		s.items = loaded
	}
}

func (s *Store) saveLocked() {
	if strings.TrimSpace(s.path) == "" {
		return
	}
	raw, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err == nil {
		if err := os.Rename(tmp, s.path); err == nil {
			return
		}
		_ = os.Remove(tmp)
	}
	_ = os.WriteFile(s.path, raw, 0o644)
}

func newItemID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "chk_" + hex.EncodeToString(b[:])
}

// SyncFromConfig seeds any checklist widget that has initial items in YAML and does not yet exist in the runtime store.
func (s *Store) SyncFromConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	s.mu.Lock()
	changed := false
	seed := func(w config.WidgetConfig) {
		if w.Type != "checklist" || w.ID == "" {
			return
		}
		if _, exists := s.items[w.ID]; exists {
			return
		}
		if len(w.ChecklistItems) == 0 {
			return
		}
		cp := make([]config.ChecklistItem, len(w.ChecklistItems))
		for i, it := range w.ChecklistItems {
			id := strings.TrimSpace(it.ID)
			if id == "" {
				id = fmt.Sprintf("item_%d", i+1)
			}
			cp[i] = config.ChecklistItem{ID: id, Text: strings.TrimSpace(it.Text), Done: it.Done}
		}
		s.items[w.ID] = cp
		changed = true
	}
	for _, p := range cfg.Pages {
		for _, w := range p.Widgets {
			seed(w)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				seed(pin.Widget)
			}
		}
	}
	if changed {
		s.saveLocked()
	}
	s.mu.Unlock()
}

// Items returns a defensive copy of items for widgetID.
func (s *Store) Items(widgetID string) []config.ChecklistItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.items[widgetID]
	out := make([]config.ChecklistItem, len(src))
	copy(out, src)
	return out
}

// All returns a deep copy of all stored checklists keyed by widget ID.
func (s *Store) All() map[string][]config.ChecklistItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]config.ChecklistItem, len(s.items))
	for k, v := range s.items {
		cp := make([]config.ChecklistItem, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// Add appends a new checklist item for widgetID.
func (s *Store) Add(widgetID, text string) (config.ChecklistItem, error) {
	text = strings.TrimSpace(text)
	if strings.TrimSpace(widgetID) == "" {
		return config.ChecklistItem{}, fmt.Errorf("missing widget_id")
	}
	if text == "" {
		return config.ChecklistItem{}, fmt.Errorf("empty item text")
	}
	item := config.ChecklistItem{
		ID:   newItemID(),
		Text: text,
		Done: false,
	}
	s.mu.Lock()
	s.items[widgetID] = append(s.items[widgetID], item)
	s.saveLocked()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	return item, nil
}

// Toggle flips the Done flag of itemID under widgetID.
func (s *Store) Toggle(widgetID, itemID string) (bool, bool) {
	s.mu.Lock()
	list := s.items[widgetID]
	for i := range list {
		if list[i].ID == itemID {
			list[i].Done = !list[i].Done
			newDone := list[i].Done
			s.saveLocked()
			cb := s.onChange
			s.mu.Unlock()
			if cb != nil {
				cb()
			}
			return newDone, true
		}
	}
	s.mu.Unlock()
	return false, false
}

// Delete removes itemID from widgetID.
func (s *Store) Delete(widgetID, itemID string) bool {
	s.mu.Lock()
	list := s.items[widgetID]
	for i := range list {
		if list[i].ID == itemID {
			s.items[widgetID] = append(list[:i], list[i+1:]...)
			if s.items[widgetID] == nil {
				s.items[widgetID] = []config.ChecklistItem{}
			}
			s.saveLocked()
			cb := s.onChange
			s.mu.Unlock()
			if cb != nil {
				cb()
			}
			return true
		}
	}
	s.mu.Unlock()
	return false
}

// ClearDone removes all completed items from widgetID and returns the count removed.
func (s *Store) ClearDone(widgetID string) int {
	s.mu.Lock()
	list := s.items[widgetID]
	kept := make([]config.ChecklistItem, 0, len(list))
	removed := 0
	for _, it := range list {
		if it.Done {
			removed++
		} else {
			kept = append(kept, it)
		}
	}
	if removed == 0 {
		s.mu.Unlock()
		return 0
	}
	s.items[widgetID] = kept
	s.saveLocked()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	return removed
}

// Replace replaces the complete list of items for widgetID (used by the web editor).
func (s *Store) Replace(widgetID string, items []config.ChecklistItem) error {
	if strings.TrimSpace(widgetID) == "" {
		return fmt.Errorf("missing widget_id")
	}
	cp := make([]config.ChecklistItem, 0, len(items))
	for _, it := range items {
		txt := strings.TrimSpace(it.Text)
		if txt == "" {
			continue
		}
		id := strings.TrimSpace(it.ID)
		if id == "" {
			id = newItemID()
		}
		cp = append(cp, config.ChecklistItem{ID: id, Text: txt, Done: it.Done})
	}
	s.mu.Lock()
	s.items[widgetID] = cp
	s.saveLocked()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// FirstWidgetID finds the first configured checklist widget ID in cfg (useful for Shortcuts defaults).
func FirstWidgetID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, p := range cfg.Pages {
		for _, w := range p.Widgets {
			if w.Type == "checklist" && w.ID != "" {
				return w.ID
			}
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				if pin.Widget.Type == "checklist" && pin.Widget.ID != "" {
					return pin.Widget.ID
				}
			}
		}
	}
	return ""
}
