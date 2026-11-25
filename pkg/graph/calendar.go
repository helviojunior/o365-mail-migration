package graph

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strings"
)

// Calendar is the subset of a calendar resource we use.
type Calendar struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	IsDefaultCalendar bool   `json:"isDefaultCalendar"`
	CanEdit           bool   `json:"canEdit"`
}

// eventWritableFields are the event properties that are valid on create. The
// copy body is assembled from exactly these (an allowlist), so server-managed
// and read-only fields (id, iCalUId, organizer, changeKey, timestamps, …) are
// never sent back and cannot make the create fail.
//
// IMPORTANT: "attendees" is deliberately excluded. Microsoft Graph sends meeting
// invitations to every attendee whenever an event is created with an attendees
// array, and the docs state this "can't be configured" (there is no Graph
// equivalent of the EWS SendMeetingInvitations=SendToNone). During a migration
// the attendees already have the original meeting, so re-sending invitations
// from the new tenant would spam them. The copied event is therefore created as
// an appointment (no attendees). The original attendee list is preserved for
// reference by appendAttendeesToBody, not as real attendees.
var eventWritableFields = []string{
	"subject", "body", "start", "end", "location", "locations", "categories",
	"importance", "sensitivity", "showAs", "isAllDay", "isReminderOn",
	"reminderMinutesBeforeStart", "recurrence",
}

// eventSelect is the $select used when listing source events: the writable
// fields plus the ids needed for logic (dedup, logging, occurrence filtering).
const eventSelect = "id,iCalUId,subject,type,start,end,body,location,locations,categories,importance,sensitivity,showAs,isAllDay,isReminderOn,reminderMinutesBeforeStart,responseRequested,recurrence,hideAttendees,allowNewTimeProposals,attendees"

// ListCalendars returns the mailbox's calendars.
func (c *Client) ListCalendars(ctx context.Context) ([]Calendar, error) {
	var cals []Calendar
	next := "/me/calendars?$top=50&$select=id,name,isDefaultCalendar,canEdit"
	for next != "" {
		var page odataList[Calendar]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return nil, err
		}
		cals = append(cals, page.Value...)
		next = page.NextLink
	}
	return cals, nil
}

// CreateCalendar creates a calendar with the given name and returns it.
func (c *Client) CreateCalendar(ctx context.Context, name string) (Calendar, error) {
	var created Calendar
	err := c.postJSON(ctx, "/me/calendars", map[string]string{"name": name}, &created)
	return created, err
}

// ListEvents pages the single-instance and series-master events of a calendar
// and calls fn for each. The /events collection does not expand recurrences, so
// individual occurrences are not returned (the series master carries the
// recurrence); modified occurrences (exceptions) are not surfaced here either.
func (c *Client) ListEvents(ctx context.Context, calendarID string, fn func(map[string]interface{}) error) error {
	next := fmt.Sprintf("/me/calendars/%s/events?$top=50&$select=%s", url.PathEscape(calendarID), eventSelect)
	for next != "" {
		var page odataList[map[string]interface{}]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return err
		}
		for _, ev := range page.Value {
			if err := fn(ev); err != nil {
				return err
			}
		}
		next = page.NextLink
	}
	return nil
}

// CreateEvent creates an event in a calendar from an already-sanitized body and
// returns the new event id.
func (c *Client) CreateEvent(ctx context.Context, calendarID string, body map[string]interface{}) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/me/calendars/%s/events", url.PathEscape(calendarID))
	if err := c.postJSON(ctx, path, body, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// DeleteEvent deletes an event by id.
func (c *Client) DeleteEvent(ctx context.Context, eventID string) error {
	return c.deleteURL(ctx, "/me/events/"+url.PathEscape(eventID))
}

// EventCreateBody builds the create body for a destination event by copying only
// the writable fields from a source event. Read-only/server fields are left out,
// so the event lands as a plain copy (a new iCalUId is assigned by Graph).
//
// Attendees are NOT copied as real attendees (that would send invitations); the
// original list is appended to the event body for reference instead.
func EventCreateBody(src map[string]interface{}) map[string]interface{} {
	body := make(map[string]interface{}, len(eventWritableFields)+1)
	for _, k := range eventWritableFields {
		if v, ok := src[k]; ok && v != nil {
			body[k] = v
		}
	}
	appendAttendeesToBody(body, src)
	return body
}

// appendAttendeesToBody records the source event's attendees as text in the
// event body, so the information is kept without creating real attendees (which
// would trigger invitation emails). It is a no-op when there are no attendees.
func appendAttendeesToBody(body, src map[string]interface{}) {
	list := attendeeList(src)
	if list == "" {
		return
	}
	if b, ok := body["body"].(map[string]interface{}); ok {
		content, _ := b["content"].(string)
		if ct, _ := b["contentType"].(string); strings.EqualFold(ct, "html") {
			b["content"] = content + "<hr><p><b>Attendees (migrated):</b> " + html.EscapeString(list) + "</p>"
		} else {
			b["content"] = content + "\n\n----\nAttendees (migrated): " + list
		}
		return
	}
	// The source event had no body: add a plain-text one.
	body["body"] = map[string]interface{}{
		"contentType": "text",
		"content":     "Attendees (migrated): " + list,
	}
}

// attendeeList renders a source event's attendees as "Name <addr>, ...".
func attendeeList(src map[string]interface{}) string {
	raw, ok := src["attendees"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, a := range raw {
		am, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		ea, _ := am["emailAddress"].(map[string]interface{})
		name, _ := ea["name"].(string)
		addr, _ := ea["address"].(string)
		switch {
		case name != "" && addr != "" && name != addr:
			parts = append(parts, fmt.Sprintf("%s <%s>", name, addr))
		case addr != "":
			parts = append(parts, addr)
		case name != "":
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}
