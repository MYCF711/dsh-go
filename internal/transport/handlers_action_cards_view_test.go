package transport

import "testing"

func TestActionCardsViewHook(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, ok, _ := srv.officialActionCardsView("s"); ok {
		t.Fatal("未挂载时不应声称有 actionCards 投影")
	}
	srv.AttachActionCardsView(func(sessionID string) (map[string]any, error) {
		return map[string]any{"proposals": map[string]any{}, "decisions": map[string]any{}}, nil
	})
	value, ok, err := srv.officialActionCardsView("s")
	if err != nil || !ok || value == nil {
		t.Fatalf("hook = %v %v %v", value, ok, err)
	}
	srv.DetachActionCardsView()
	if _, ok, _ := srv.officialActionCardsView("s"); ok {
		t.Fatal("Detach 后仍声称有投影")
	}
}
