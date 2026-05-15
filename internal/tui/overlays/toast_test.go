package overlays

import (
	"testing"
	"time"

	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/billygate/kap-toolsbox/internal/tui/themes"
)

func newTestStyles() *styles.Styles {
	p, _ := themes.Get("catppuccin")
	return styles.New(p)
}

func TestToastsPushAndEmpty(t *testing.T) {
	var q Toasts
	if !q.Empty() {
		t.Error("fresh Toasts should be empty")
	}
	q.Push(ToastInfo, "hello")
	if q.Empty() {
		t.Error("Toasts should not be empty after Push")
	}
}

func TestToastsTickExpiresEntries(t *testing.T) {
	var q Toasts
	q.Push(ToastError, "oh no")

	// Tick at a time well in the future — all entries should expire.
	q.Tick(time.Now().Add(10 * time.Second))
	if !q.Empty() {
		t.Error("all entries should have been expired after Tick")
	}
}

func TestToastsTickKeepsActiveEntries(t *testing.T) {
	var q Toasts
	q.Push(ToastSuccess, "ok")

	// Tick at exactly now — entries expire at now+TTL so they should survive.
	q.Tick(time.Now())
	if q.Empty() {
		t.Error("entries should still be active when Tick is called at push time")
	}
}

func TestToastsViewEmptyReturnsEmpty(t *testing.T) {
	var q Toasts
	s := newTestStyles()
	if v := q.View(80, s); v != "" {
		t.Errorf("View on empty Toasts = %q, want empty string", v)
	}
}

func TestToastsViewRendersAllKinds(t *testing.T) {
	var q Toasts
	q.Push(ToastInfo, "info msg")
	q.Push(ToastSuccess, "success msg")
	q.Push(ToastError, "error msg")

	s := newTestStyles()
	view := q.View(80, s)
	if view == "" {
		t.Error("View should return non-empty string when there are toasts")
	}
}
