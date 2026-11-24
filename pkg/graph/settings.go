package graph

import (
	"context"
)

// GetInboxRules returns the inbox (server-side message) rules as decoded JSON
// objects, so the caller can strip read-only fields and remap folder ids.
func (c *Client) GetInboxRules(ctx context.Context) ([]map[string]interface{}, error) {
	var page odataList[map[string]interface{}]
	if err := c.getJSON(ctx, "/me/mailFolders/inbox/messageRules", &page); err != nil {
		return nil, err
	}
	return page.Value, nil
}

// CreateInboxRule creates one inbox rule from a (cleaned) rule object.
func (c *Client) CreateInboxRule(ctx context.Context, rule map[string]interface{}) error {
	return c.postJSON(ctx, "/me/mailFolders/inbox/messageRules", rule, nil)
}

// GetAutomaticReplies returns the mailbox automatic-replies (out-of-office)
// setting as a decoded JSON object.
func (c *Client) GetAutomaticReplies(ctx context.Context) (map[string]interface{}, error) {
	var setting map[string]interface{}
	if err := c.getJSON(ctx, "/me/mailboxSettings/automaticRepliesSetting", &setting); err != nil {
		return nil, err
	}
	return setting, nil
}

// SetAutomaticReplies applies an automatic-replies setting to the mailbox.
func (c *Client) SetAutomaticReplies(ctx context.Context, setting map[string]interface{}) error {
	body := map[string]interface{}{"automaticRepliesSetting": setting}
	return c.patchJSON(ctx, "/me/mailboxSettings", body)
}
