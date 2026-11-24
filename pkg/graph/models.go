package graph

// User is the subset of /me we care about.
type User struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// MailFolder is the subset of a mailFolder resource we use.
type MailFolder struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ParentFolderID   string `json:"parentFolderId"`
	ChildFolderCount int    `json:"childFolderCount"`
	TotalItemCount   int    `json:"totalItemCount"`
	WellKnownName    string `json:"wellKnownName"`

	// Path is computed by the client (not returned by Graph): full display
	// path from the root, e.g. "Inbox/Projects/2024".
	Path string `json:"-"`
}

// FollowupFlag mirrors the message flag object.
type FollowupFlag struct {
	FlagStatus string `json:"flagStatus,omitempty"` // notFlagged | complete | flagged
}

// Message is the subset of a message resource used to drive the copy.
type Message struct {
	ID                string        `json:"id"`
	InternetMessageID string        `json:"internetMessageId"`
	Subject           string        `json:"subject"`
	IsRead            bool          `json:"isRead"`
	Flag              *FollowupFlag `json:"flag,omitempty"`
	Categories        []string      `json:"categories,omitempty"`
}

// patchBody is what we PATCH onto a freshly imported message to restore the
// metadata that MIME import does not carry.
type patchBody struct {
	IsRead     bool          `json:"isRead"`
	Flag       *FollowupFlag `json:"flag,omitempty"`
	Categories []string      `json:"categories,omitempty"`
}

// odataList is the generic Graph collection envelope.
type odataList[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}
