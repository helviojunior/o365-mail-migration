package graph

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/helviojunior/mail_migration/pkg/log"
)

// Me returns the signed-in user (identity of the cached account).
func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	if err := c.getJSON(ctx, "/me?$select=id,displayName,mail,userPrincipalName", &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// folderSelect lists mailFolder properties valid on the Graph v1.0 endpoint.
// Note: wellKnownName only exists on the beta endpoint, so it is resolved
// separately via ResolveWellKnownIDs instead of being $select-ed here.
const folderSelect = "id,displayName,parentFolderId,childFolderCount,totalItemCount"

// wellKnownFolderNames are the special folders we map directly between tenants.
// Each can be addressed by name as a folder id (e.g. GET /me/mailFolders/inbox).
var wellKnownFolderNames = []string{
	"inbox", "archive", "deleteditems", "junkemail",
	"sentitems", "drafts", "outbox", "conversationhistory",
}

// ListFolders returns every mail folder (including hidden ones) with a computed
// display Path, ordered parents-before-children so a destination tree can be
// created top-down. Well-known folders are annotated via their resolved ids.
func (c *Client) ListFolders(ctx context.Context) ([]MailFolder, error) {
	wk := c.ResolveWellKnownIDs(ctx) // best effort; empty map is fine

	var out []MailFolder
	roots, err := c.pageFolders(ctx, "/me/mailFolders?includeHiddenFolders=true&$top=100&$select="+folderSelect)
	if err != nil {
		return nil, err
	}
	for _, f := range roots {
		f.Path = f.DisplayName
		out = append(out, f)
		if f.ChildFolderCount > 0 {
			if err := c.walkChildren(ctx, f, &out); err != nil {
				return nil, err
			}
		}
	}

	for i := range out {
		if name, ok := wk[out[i].ID]; ok {
			out[i].WellKnownName = name
		}
	}
	return out, nil
}

// ResolveWellKnownIDs maps each special folder's real id to its well-known name
// by addressing it by name. Folders absent from the mailbox are skipped. This
// replaces the beta-only wellKnownName property on the v1.0 endpoint.
func (c *Client) ResolveWellKnownIDs(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, name := range wellKnownFolderNames {
		var f MailFolder
		if err := c.getJSON(ctx, "/me/mailFolders/"+name+"?$select=id,displayName", &f); err != nil {
			log.Debugf("well-known folder %q not resolved: %v", name, err)
			continue
		}
		if f.ID != "" {
			out[f.ID] = name
		}
	}
	return out
}

func (c *Client) walkChildren(ctx context.Context, parent MailFolder, out *[]MailFolder) error {
	path := fmt.Sprintf("/me/mailFolders/%s/childFolders?includeHiddenFolders=true&$top=100&$select=%s",
		url.PathEscape(parent.ID), folderSelect)
	children, err := c.pageFolders(ctx, path)
	if err != nil {
		return err
	}
	for _, ch := range children {
		ch.Path = parent.Path + "/" + ch.DisplayName
		*out = append(*out, ch)
		if ch.ChildFolderCount > 0 {
			if err := c.walkChildren(ctx, ch, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) pageFolders(ctx context.Context, path string) ([]MailFolder, error) {
	var all []MailFolder
	next := path
	for next != "" {
		var page odataList[MailFolder]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		next = page.NextLink
	}
	return all, nil
}

// ListMessages invokes fn for every message in folderID, following pagination.
func (c *Client) ListMessages(ctx context.Context, folderID string, fn func(Message) error) error {
	next := fmt.Sprintf("/me/mailFolders/%s/messages?$top=50&$select=id,internetMessageId,subject,isRead,flag,categories,receivedDateTime,sentDateTime",
		url.PathEscape(folderID))
	for next != "" {
		var page odataList[Message]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return err
		}
		for _, m := range page.Value {
			if err := fn(m); err != nil {
				return err
			}
		}
		next = page.NextLink
	}
	return nil
}

// GetMIME downloads the raw RFC822 MIME for a message (preserves original
// headers, body and attachments).
func (c *Client) GetMIME(ctx context.Context, msgID string) ([]byte, error) {
	return c.getRaw(ctx, fmt.Sprintf("/me/messages/%s/$value", url.PathEscape(msgID)))
}

// ImportMIME creates a message in destFolderID from raw MIME and returns the
// new message id. Graph expects Content-Type: text/plain with a base64 body.
func (c *Client) ImportMIME(ctx context.Context, destFolderID string, mime []byte) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(mime)
	path := fmt.Sprintf("/me/mailFolders/%s/messages", url.PathEscape(destFolderID))
	data, err := c.postRaw(ctx, path, "text/plain", []byte(b64))
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := jsonUnmarshal(data, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// PatchMessageMeta restores metadata that MIME import does not carry: read
// state, follow-up flag, categories and the message flags (so an imported
// message is not left as a draft). When fixDates is true, it also sets the
// original received/sent timestamps via MAPI extended properties (best effort:
// some tenants ignore writing these on an existing message — use VerifyDates to
// detect that). isDraft keeps genuine Drafts-folder items marked as drafts.
func (c *Client) PatchMessageMeta(ctx context.Context, msgID string, m Message, fixDates, isDraft bool) error {
	body := buildPatchBody(m, fixDates, isDraft)
	return c.patchJSON(ctx, fmt.Sprintf("/me/messages/%s", url.PathEscape(msgID)), body)
}

// buildPatchBody assembles the PATCH payload. It is a pure function so the
// extended-property wiring can be unit-tested without a live mailbox.
func buildPatchBody(m Message, fixDates, isDraft bool) patchBody {
	body := patchBody{IsRead: m.IsRead, Flag: m.Flag, Categories: m.Categories}

	// PR_MESSAGE_FLAGS: mark read and, unless this is a real draft, clear the
	// draft (unsent) flag that MIME import leaves set.
	flags := 0
	if m.IsRead {
		flags |= msgFlagRead
	}
	if isDraft {
		flags |= msgFlagUnsent
	}
	body.SingleValueExtendedProperties = append(body.SingleValueExtendedProperties,
		singleValueExtendedProperty{ID: propMessageFlags, Value: strconv.Itoa(flags)})

	if fixDates {
		if m.SentDateTime != "" {
			body.SingleValueExtendedProperties = append(body.SingleValueExtendedProperties,
				singleValueExtendedProperty{ID: propClientSubmitTime, Value: m.SentDateTime})
		}
		if m.ReceivedDateTime != "" {
			body.SingleValueExtendedProperties = append(body.SingleValueExtendedProperties,
				singleValueExtendedProperty{ID: propMessageDeliveryTime, Value: m.ReceivedDateTime})
		}
	}
	return body
}

// ListInternetMessageIDs returns the set of internetMessageId values already
// present in a destination folder, for idempotent de-duplication across runs
// (independent of the local state DB).
func (c *Client) ListInternetMessageIDs(ctx context.Context, folderID string) (map[string]bool, error) {
	ids := map[string]bool{}
	next := fmt.Sprintf("/me/mailFolders/%s/messages?$top=100&$select=internetMessageId", url.PathEscape(folderID))
	for next != "" {
		var page odataList[Message]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Value {
			if m.InternetMessageID != "" {
				ids[m.InternetMessageID] = true
			}
		}
		next = page.NextLink
	}
	return ids, nil
}

// GetMessageDates reads back the received/sent timestamps of a message (used to
// verify date fidelity after a copy).
func (c *Client) GetMessageDates(ctx context.Context, msgID string) (received, sent string, err error) {
	var d messageDates
	if err = c.getJSON(ctx, fmt.Sprintf("/me/messages/%s?$select=receivedDateTime,sentDateTime", url.PathEscape(msgID)), &d); err != nil {
		return "", "", err
	}
	return d.ReceivedDateTime, d.SentDateTime, nil
}

// CreateChildFolder creates a child folder and returns it.
func (c *Client) CreateChildFolder(ctx context.Context, parentID, displayName string) (MailFolder, error) {
	var created MailFolder
	path := fmt.Sprintf("/me/mailFolders/%s/childFolders", url.PathEscape(parentID))
	err := c.postJSON(ctx, path, map[string]string{"displayName": displayName}, &created)
	return created, err
}

// FindWellKnownFolder resolves a destination well-known folder (inbox, archive,
// deleteditems, junkemail, sentitems, drafts, outbox, ...) by its name.
func (c *Client) FindWellKnownFolder(ctx context.Context, wellKnownName string) (MailFolder, error) {
	var f MailFolder
	err := c.getJSON(ctx, "/me/mailFolders/"+url.PathEscape(wellKnownName)+"?$select="+folderSelect, &f)
	return f, err
}

// FindChildByName returns the first child folder of parentID whose displayName
// matches name (case-insensitive), or ok=false when none exists.
func (c *Client) FindChildByName(ctx context.Context, parentID, name string) (MailFolder, bool, error) {
	children, err := c.pageFolders(ctx,
		fmt.Sprintf("/me/mailFolders/%s/childFolders?includeHiddenFolders=true&$top=100&$select=%s",
			url.PathEscape(parentID), folderSelect))
	if err != nil {
		return MailFolder{}, false, err
	}
	for _, ch := range children {
		if strings.EqualFold(ch.DisplayName, name) {
			return ch, true, nil
		}
	}
	return MailFolder{}, false, nil
}

// DeleteMessage deletes a message (moves it to Deleted Items / Recoverable
// Items, like any Graph delete).
func (c *Client) DeleteMessage(ctx context.Context, msgID string) error {
	return c.deleteURL(ctx, "/me/messages/"+url.PathEscape(msgID))
}

// MailboxRoot returns the id of the msgfolderroot (top of the mailbox tree), so
// top-level source folders can be recreated under it in the destination.
func (c *Client) MailboxRoot(ctx context.Context) (string, error) {
	f, err := c.FindWellKnownFolder(ctx, "msgfolderroot")
	if err != nil {
		return "", err
	}
	return f.ID, nil
}
