package graph

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPatchBody(t *testing.T) {
	msg := Message{
		IsRead:           true,
		Categories:       []string{"Blue"},
		ReceivedDateTime: "2024-01-15T10:30:00Z",
		SentDateTime:     "2024-01-15T10:25:00Z",
	}

	t.Run("always sets message flags: read and not a draft", func(t *testing.T) {
		body := buildPatchBody(msg, false, false)
		if len(body.SingleValueExtendedProperties) != 1 {
			t.Fatalf("expected 1 extended property (message flags), got %d", len(body.SingleValueExtendedProperties))
		}
		p := body.SingleValueExtendedProperties[0]
		if p.ID != propMessageFlags || p.Value != "1" { // MSGFLAG_READ, not unsent
			t.Fatalf("message-flags property wrong: %+v", p)
		}
		if !body.IsRead {
			t.Fatal("isRead should be carried through")
		}
	})

	t.Run("draft keeps the unsent flag", func(t *testing.T) {
		body := buildPatchBody(Message{IsRead: false}, false, true)
		if p := body.SingleValueExtendedProperties[0]; p.Value != "8" { // MSGFLAG_UNSENT only
			t.Fatalf("draft flags wrong: %+v", p)
		}
	})

	t.Run("with fix-dates appends submit then delivery time", func(t *testing.T) {
		body := buildPatchBody(msg, true, false)
		props := body.SingleValueExtendedProperties
		if len(props) != 3 { // flags + submit + delivery
			t.Fatalf("expected 3 extended properties, got %d", len(props))
		}
		if props[1].ID != propClientSubmitTime || props[1].Value != msg.SentDateTime {
			t.Fatalf("submit-time property wrong: %+v", props[1])
		}
		if props[2].ID != propMessageDeliveryTime || props[2].Value != msg.ReceivedDateTime {
			t.Fatalf("delivery-time property wrong: %+v", props[2])
		}

		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, want := range []string{`"Integer 0x0E07"`, `"SystemTime 0x0039"`, `"SystemTime 0x0E06"`} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("marshaled body missing %s: %s", want, raw)
			}
		}
	})

	t.Run("fix-dates with missing timestamps keeps only flags", func(t *testing.T) {
		body := buildPatchBody(Message{IsRead: true}, true, false)
		if len(body.SingleValueExtendedProperties) != 1 {
			t.Fatalf("expected only the flags property when dates empty, got %d", len(body.SingleValueExtendedProperties))
		}
	})
}
