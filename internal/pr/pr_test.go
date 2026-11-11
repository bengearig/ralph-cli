package pr

import "testing"

func TestLinkUnregistered(t *testing.T) {
	if got, ok := Link("no_such_provider", "a", "b", 1); ok || got != "" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}

func TestLinkNilFunc(t *testing.T) {
	RegisterLink("test_nil_link", nil)
	if got, ok := Link("test_nil_link", "a", "b", 1); ok || got != "" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}

func TestRegisterLinkDuplicatePanics(t *testing.T) {
	RegisterLink("test_dup_link", func(string, string, int) string { return "x" })
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	RegisterLink("test_dup_link", func(string, string, int) string { return "y" })
}

func TestLinkRegistered(t *testing.T) {
	RegisterLink("test_ok_link", func(ns, repo string, n int) string { return ns + "/" + repo })
	if got, ok := Link("test_ok_link", "a", "b", 3); !ok || got != "a/b" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}
