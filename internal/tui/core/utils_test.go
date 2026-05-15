package core

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
)

type stubItem struct{ s string }

func (i stubItem) FilterValue() string { return i.s }

// ── ListItem helpers ──────────────────────────────────────────────────────

func TestItemConstructorIsNormal(t *testing.T) {
	li := Item("hello")
	if li.Text != "hello" {
		t.Errorf("Text = %q, want hello", li.Text)
	}
	if li.IsSeparator() {
		t.Error("Item should not be a separator")
	}
	if li.FilterValue() != "hello" {
		t.Errorf("FilterValue = %q, want hello", li.FilterValue())
	}
}

func TestSeparatorConstructor(t *testing.T) {
	sep := Separator("section")
	if !sep.IsSeparator() {
		t.Error("Separator should report IsSeparator() == true")
	}
	if sep.FilterValue() != "" {
		t.Errorf("separator FilterValue should be empty, got %q", sep.FilterValue())
	}
}

func TestAsListItemOK(t *testing.T) {
	li := Item("test")
	result, ok := AsListItem(li)
	if !ok {
		t.Fatal("AsListItem should succeed for a ListItem")
	}
	if result.Text != "test" {
		t.Errorf("Text = %q, want test", result.Text)
	}
}

func TestAsListItemFail(t *testing.T) {
	_, ok := AsListItem(stubItem{s: "not a ListItem"})
	if ok {
		t.Error("AsListItem should fail for a non-ListItem")
	}
}

// ── KeyMap helpers ────────────────────────────────────────────────────────

func TestKeysShortHelpHasFiveEntries(t *testing.T) {
	help := Keys.ShortHelp()
	if len(help) != 5 {
		t.Errorf("ShortHelp len = %d, want 5", len(help))
	}
}

func TestKeysFullHelpHasThreeRows(t *testing.T) {
	help := Keys.FullHelp()
	if len(help) != 3 {
		t.Errorf("FullHelp rows = %d, want 3", len(help))
	}
}

func TestTickReturnsNonNilCmd(t *testing.T) {
	cmd := Tick()
	if cmd == nil {
		t.Error("Tick() should return a non-nil tea.Cmd")
	}
}

func TestShouldShowTwoColumnsBelowThreshold(t *testing.T) {
	items := make([]list.Item, 5)
	for i := range items {
		items[i] = stubItem{s: "short"}
	}
	if ShouldShowTwoColumns(200, items) {
		t.Error("5 items should not trigger two-column mode (below 10-item threshold)")
	}
}

func TestShouldShowTwoColumnsAboveThreshold(t *testing.T) {
	items := make([]list.Item, 30)
	for i := range items {
		items[i] = stubItem{s: "shortname"}
	}
	if !ShouldShowTwoColumns(200, items) {
		t.Error("30 short items at width=200 should trigger two-column mode")
	}
}

func TestShouldShowTwoColumnsTooNarrow(t *testing.T) {
	items := make([]list.Item, 30)
	for i := range items {
		items[i] = stubItem{s: "very-long-pod-name-with-many-characters-1234567890"}
	}
	if ShouldShowTwoColumns(60, items) {
		t.Error("30 long items at width=60 should not trigger two-column mode")
	}
}

func TestShouldShowTwoColumnsExactlyAtThreshold(t *testing.T) {
	// 10 items is the minimum; verify it's inclusive (len < 10 returns false)
	items9 := make([]list.Item, 9)
	for i := range items9 {
		items9[i] = stubItem{s: "x"}
	}
	if ShouldShowTwoColumns(1000, items9) {
		t.Error("9 items should not trigger two-column mode (threshold is 10)")
	}

	items10 := make([]list.Item, 10)
	for i := range items10 {
		items10[i] = stubItem{s: "x"}
	}
	// "x" is 1 char wide; (1+10)*2 = 22 < 1000-10 = 990 → should be true
	if !ShouldShowTwoColumns(1000, items10) {
		t.Error("10 single-char items at width=1000 should trigger two-column mode")
	}
}
