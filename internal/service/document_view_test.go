package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func TestDocumentViewOrigin(t *testing.T) {
	origin := int64(7)
	d := domain.Document{ID: 3, Kind: domain.DocNote, Slug: "n", Title: "N"}

	b, err := json.Marshal(documentView(d, false, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "origin_initiative_id") {
		t.Errorf("origin shown without one: %s", b)
	}

	d.OriginInitiativeID = &origin
	v := documentView(d, false, false)
	if v.OriginInitiativeID != "I-7" {
		t.Errorf("OriginInitiativeID = %q, want I-7", v.OriginInitiativeID)
	}
	b, _ = json.Marshal(v)
	if !strings.Contains(string(b), `"origin_initiative_id":"I-7"`) {
		t.Errorf("json = %s", b)
	}
}
