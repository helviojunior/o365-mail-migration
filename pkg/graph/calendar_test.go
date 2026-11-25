package graph

import (
	"strings"
	"testing"
)

func TestEventCreateBody(t *testing.T) {
	src := map[string]interface{}{
		// Writable — must be copied.
		"subject":    "Planning",
		"start":      map[string]interface{}{"dateTime": "2026-01-02T10:00:00", "timeZone": "UTC"},
		"end":        map[string]interface{}{"dateTime": "2026-01-02T11:00:00", "timeZone": "UTC"},
		"isAllDay":   false,
		"categories": []interface{}{"Work"},
		"recurrence": map[string]interface{}{"pattern": map[string]interface{}{"type": "weekly"}},
		"body":       map[string]interface{}{"contentType": "html", "content": "<p>Agenda</p>"},
		// Attendees — must NOT become real attendees (would send invites).
		"attendees": []interface{}{
			map[string]interface{}{"emailAddress": map[string]interface{}{"name": "Alice", "address": "alice@x.com"}, "type": "required"},
		},
		// Read-only / server-managed — must be dropped.
		"id":              "AAA",
		"iCalUId":         "040000...",
		"organizer":       map[string]interface{}{"emailAddress": map[string]interface{}{"address": "a@b.com"}},
		"changeKey":       "xyz",
		"createdDateTime": "2026-01-01T00:00:00Z",
		"webLink":         "https://outlook.office365.com/...",
		"@odata.etag":     "W/abc",
		"seriesMasterId":  "BBB",
		// Present but nil — must be dropped.
		"location": nil,
	}

	body := EventCreateBody(src)

	mustHave := []string{"subject", "start", "end", "isAllDay", "categories", "recurrence", "body"}
	for _, k := range mustHave {
		if _, ok := body[k]; !ok {
			t.Errorf("writable field %q was not copied", k)
		}
	}

	mustDrop := []string{
		"attendees", // crucial: no real attendees -> no invitation emails
		"id", "iCalUId", "organizer", "changeKey", "createdDateTime", "webLink",
		"@odata.etag", "seriesMasterId", "location", // location is nil -> dropped
	}
	for _, k := range mustDrop {
		if _, ok := body[k]; ok {
			t.Errorf("field %q should not be in the create body", k)
		}
	}

	// The attendee list is preserved as text in the body instead.
	b, _ := body["body"].(map[string]interface{})
	content, _ := b["content"].(string)
	if !strings.Contains(content, "alice@x.com") {
		t.Errorf("attendee not preserved in body: %q", content)
	}
}
