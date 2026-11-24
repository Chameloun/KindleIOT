package checklist

import (
	"path/filepath"
	"testing"

	"kindleiot/internal/config"
)

func TestChecklistPersistenceAndOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checklists.json")
	changes := 0
	st := NewStore(path, func() { changes++ })

	cfg := &config.Config{
		Pages: []config.PageConfig{
			{
				ID: "p1",
				Widgets: []config.WidgetConfig{
					{
						ID:    "shop",
						Type:  "checklist",
						Label: "Shopping",
						ChecklistItems: []config.ChecklistItem{
							{ID: "i1", Text: "Milk", Done: false},
							{ID: "i2", Text: "Bread", Done: true},
						},
					},
				},
			},
		},
	}
	st.SyncFromConfig(cfg)
	if FirstWidgetID(cfg) != "shop" {
		t.Fatalf("expected FirstWidgetID=shop, got %q", FirstWidgetID(cfg))
	}

	items := st.Items("shop")
	if len(items) != 2 || items[0].Text != "Milk" {
		t.Fatalf("unexpected seeded items: %+v", items)
	}

	// Toggle i1 -> Done=true
	done, ok := st.Toggle("shop", "i1")
	if !ok || !done {
		t.Fatalf("expected i1 toggled to done=true, ok=%v done=%v", ok, done)
	}

	// Add third item
	added, err := st.Add("shop", "Coffee")
	if err != nil || added.Text != "Coffee" {
		t.Fatalf("Add failed: %v %+v", err, added)
	}

	// ClearDone should remove i1 and i2, leaving Coffee
	removed := st.ClearDone("shop")
	if removed != 2 {
		t.Fatalf("expected 2 removed by ClearDone, got %d", removed)
	}

	// Reload from disk and run SyncFromConfig again -> user state preserved!
	st2 := NewStore(path, nil)
	st2.SyncFromConfig(cfg)
	items2 := st2.Items("shop")
	if len(items2) != 1 || items2[0].Text != "Coffee" {
		t.Fatalf("expected persisted [Coffee] after SyncFromConfig, got %+v", items2)
	}

	if !st2.Delete("shop", items2[0].ID) {
		t.Fatalf("expected Delete to return true")
	}
	if len(st2.Items("shop")) != 0 {
		t.Fatalf("expected empty list after Delete")
	}
	if changes == 0 {
		t.Fatalf("expected onChange callbacks")
	}
}
