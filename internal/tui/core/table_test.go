package core

import (
	"testing"

	"github.com/charmbracelet/bubbles/table"

	"github.com/billygate/kapctl/internal/tui/styles"
	"github.com/billygate/kapctl/internal/tui/themes"
)

func newTestTableStyles() *styles.Styles {
	p, _ := themes.Get("catppuccin")
	return styles.New(p)
}

// noncomparable embeds a slice field so any struct containing it is
// not eligible for `==` comparison; comparing two interface values
// wrapping noncomparable would panic at runtime. Table.refilter must
// not rely on that comparison.
type noncomparable struct {
	Name  string
	Ports []int
}

func (n noncomparable) Cells() table.Row    { return table.Row{n.Name} }
func (n noncomparable) FilterValue() string { return n.Name }

func TestTableRefilterPreservesCursorWithNonComparableItems(t *testing.T) {
	s := newTestTableStyles()
	tt := NewTable(s, []table.Column{{Title: "NAME", Width: 20}})
	tt.SetSize(40, 5)

	first := []RowProvider{
		noncomparable{Name: "alpha", Ports: []int{1, 2}},
		noncomparable{Name: "beta", Ports: []int{3, 4}},
		noncomparable{Name: "gamma", Ports: []int{5, 6}},
	}
	tt.SetItems(first)
	// Move cursor to the second visible row.
	if got := tt.SelectedItem(); got == nil || got.FilterValue() != "alpha" {
		t.Fatalf("initial selection = %v, want alpha", got)
	}

	// Simulate a refresh where the same names re-appear with different
	// underlying data. SetItems triggers refilter(true) — without the
	// FilterValue-based identity check, this would panic at runtime
	// because slices in the embedded structs make `==` non-applicable.
	second := []RowProvider{
		noncomparable{Name: "alpha", Ports: []int{7, 8}},
		noncomparable{Name: "beta", Ports: []int{9, 10}},
		noncomparable{Name: "gamma", Ports: []int{11, 12}},
	}
	tt.SetItems(second)

	if got := tt.SelectedItem(); got == nil || got.FilterValue() != "alpha" {
		t.Errorf("after refresh: selection = %v, want alpha (cursor preserved by FilterValue)", got)
	}
}

func TestTableSetItemsToEmptyDoesNotPanic(t *testing.T) {
	s := newTestTableStyles()
	tt := NewTable(s, []table.Column{{Title: "NAME", Width: 20}})
	tt.SetSize(40, 5)

	tt.SetItems([]RowProvider{
		noncomparable{Name: "alpha", Ports: []int{1}},
	})
	// The cursor is on index 0 inside the visible slice. Removing the
	// only entry must not panic when refilter compares the prior
	// selection against the new (empty) allItems.
	tt.SetItems(nil)

	if got := tt.SelectedItem(); got != nil {
		t.Errorf("SelectedItem on empty table = %v, want nil", got)
	}
	if tt.Len() != 0 {
		t.Errorf("Len on empty table = %d, want 0", tt.Len())
	}
}

// A SetItems with an empty slice clamps the inner cursor to -1; a
// later SetItems with data must land the cursor back on row 0 so
// SelectedItem() works without requiring a movement key first.
func TestTableSetItemsRecoversCursorAfterEmpty(t *testing.T) {
	s := newTestTableStyles()
	tt := NewTable(s, []table.Column{{Title: "NAME", Width: 20}})
	tt.SetSize(40, 5)

	tt.SetItems(nil) // e.g. step entered before the async load completes
	tt.SetItems([]RowProvider{noncomparable{Name: "pg-0"}})

	sel := tt.SelectedItem()
	if sel == nil {
		t.Fatal("SelectedItem() = nil after data arrived, want first row")
	}
	if sel.FilterValue() != "pg-0" {
		t.Errorf("SelectedItem() = %q, want pg-0", sel.FilterValue())
	}
}

func TestTableSetItemsAndSelected(t *testing.T) {
	s := newTestTableStyles()
	tt := NewTable(s, []table.Column{{Title: "NAME", Width: 20}}, WithRowNumbers())
	tt.SetSize(40, 5)

	tt.SetItems([]RowProvider{
		noncomparable{Name: "one"},
		noncomparable{Name: "two"},
	})
	if tt.Len() != 2 {
		t.Errorf("Len = %d, want 2", tt.Len())
	}
	sel := tt.SelectedItem()
	if sel == nil || sel.FilterValue() != "one" {
		t.Errorf("SelectedItem = %v, want one", sel)
	}
}
