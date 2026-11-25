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

	t.Run("no fix-dates sets no extended properties (draft flag is not touched)", func(t *testing.T) {
		body := buildPatchBody(msg, false)
		if len(body.SingleValueExtendedProperties) != 0 {
			t.Fatalf("expected no extended properties, got %d", len(body.SingleValueExtendedProperties))
		}
		if !body.IsRead {
			t.Fatal("isRead should be carried through")
		}
	})

	t.Run("with fix-dates sets submit then delivery time", func(t *testing.T) {
		body := buildPatchBody(msg, true)
		props := body.SingleValueExtendedProperties
		if len(props) != 2 { // submit + delivery
			t.Fatalf("expected 2 extended properties, got %d", len(props))
		}
		if props[0].ID != propClientSubmitTime || props[0].Value != msg.SentDateTime {
			t.Fatalf("submit-time property wrong: %+v", props[0])
		}
		if props[1].ID != propMessageDeliveryTime || props[1].Value != msg.ReceivedDateTime {
			t.Fatalf("delivery-time property wrong: %+v", props[1])
		}

		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, want := range []string{`"SystemTime 0x0039"`, `"SystemTime 0x0E06"`} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("marshaled body missing %s: %s", want, raw)
			}
		}
	})

	t.Run("fix-dates with missing timestamps sets no extended properties", func(t *testing.T) {
		body := buildPatchBody(Message{IsRead: true}, true)
		if len(body.SingleValueExtendedProperties) != 0 {
			t.Fatalf("expected no extended properties when dates empty, got %d", len(body.SingleValueExtendedProperties))
		}
	})
}
