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
	ReceivedDateTime  string        `json:"receivedDateTime,omitempty"`
	SentDateTime      string        `json:"sentDateTime,omitempty"`
}

// MAPI property tags (proptag id format "{type} {proptag}") used to force the
// original timestamps when Graph does not derive them from the MIME headers:
//
//	PR_CLIENT_SUBMIT_TIME    0x0039 -> sentDateTime
//	PR_MESSAGE_DELIVERY_TIME 0x0E06 -> receivedDateTime
const (
	propClientSubmitTime    = "SystemTime 0x0039"
	propMessageDeliveryTime = "SystemTime 0x0E06"
)

// singleValueExtendedProperty is one entry of a message's
// singleValueExtendedProperties collection.
type singleValueExtendedProperty struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// patchBody is what we PATCH onto a freshly imported message to restore the
// metadata that MIME import does not carry (read state, flag, categories) and,
// optionally, the original received/sent timestamps via extended properties.
type patchBody struct {
	IsRead                        bool                          `json:"isRead"`
	Flag                          *FollowupFlag                 `json:"flag,omitempty"`
	Categories                    []string                      `json:"categories,omitempty"`
	SingleValueExtendedProperties []singleValueExtendedProperty `json:"singleValueExtendedProperties,omitempty"`
}

// messageDates is the minimal projection used to verify timestamps after a copy.
type messageDates struct {
	ReceivedDateTime string `json:"receivedDateTime"`
	SentDateTime     string `json:"sentDateTime"`
}

// odataList is the generic Graph collection envelope.
type odataList[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}
