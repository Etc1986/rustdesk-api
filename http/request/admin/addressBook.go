package admin

import (
	"encoding/json"
	"sort"

	"github.com/lejianwen/rustdesk-api/v2/model"
)

type AddressBookForm struct {
	RowId            uint     `json:"row_id"`
	Id               string   `json:"id" validate:"required"`
	Username         string   `json:"username" `
	Password         string   `json:"password" `
	Hostname         string   `json:"hostname" `
	Alias            string   `json:"alias" `
	Platform         string   `json:"platform" `
	Tags             []string `json:"tags"`
	Hash             string   `json:"hash"`
	UserId           uint     `json:"user_id"`
	UserIds          []uint   `json:"user_ids"`
	ForceAlwaysRelay bool     `json:"forceAlwaysRelay"`
	RdpPort          string   `json:"rdpPort"`
	RdpUsername      string   `json:"rdpUsername"`
	Online           bool     `json:"online"`
	LoginName        string   `json:"loginName" `
	SameServer       bool     `json:"sameServer"`
	CollectionId     uint     `json:"collection_id"`
}

func (a AddressBookForm) ToAddressBook() *model.AddressBook {
	//tags转换
	tags, _ := json.Marshal(a.Tags)

	return &model.AddressBook{
		RowId:            a.RowId,
		Id:               a.Id,
		Username:         a.Username,
		Password:         a.Password,
		Hostname:         a.Hostname,
		Alias:            a.Alias,
		Platform:         a.Platform,
		Tags:             tags,
		Hash:             a.Hash,
		UserId:           a.UserId,
		ForceAlwaysRelay: a.ForceAlwaysRelay,
		RdpPort:          a.RdpPort,
		RdpUsername:      a.RdpUsername,
		Online:           a.Online,
		LoginName:        a.LoginName,
		SameServer:       a.SameServer,
		CollectionId:     a.CollectionId,
	}

}
func (a AddressBookForm) ToAddressBooks() []*model.AddressBook {
	//tags转换
	tags, _ := json.Marshal(a.Tags)

	abs := make([]*model.AddressBook, 0, len(a.UserIds))
	for _, userId := range a.UserIds {
		abs = append(abs, &model.AddressBook{
			RowId:            a.RowId,
			Id:               a.Id,
			Username:         a.Username,
			Password:         a.Password,
			Hostname:         a.Hostname,
			Alias:            a.Alias,
			Platform:         a.Platform,
			Tags:             tags,
			Hash:             a.Hash,
			UserId:           userId,
			ForceAlwaysRelay: a.ForceAlwaysRelay,
			RdpPort:          a.RdpPort,
			RdpUsername:      a.RdpUsername,
			Online:           a.Online,
			LoginName:        a.LoginName,
			SameServer:       a.SameServer,
			CollectionId:     a.CollectionId,
		})
	}
	return abs
}

// adminEditableColumns maps a request-body JSON key to the address_books column
// an administrative edit is allowed to write through it.
//
// This is an ALLOWLIST, and the inversion is the whole point. The previous
// implementation wrote with Select("*") plus an Omit() blocklist, which made
// the write-set a function of the MODEL instead of the REQUEST: every column
// the caller did not send was overwritten with its zero value. Auditing that
// field-by-field is misleading, because at struct level the admin form covers
// everything except `pinned` — so the earlier Omit("pinned") fix looked
// complete. It was not. A form field existing in Go says nothing about whether
// the client sent it: an absent JSON key decodes to the zero value, which is
// indistinguishable from "set this to empty". Every column was exposed, and
// each new one inherited the hazard.
//
// Deriving the write-set from the keys actually present in the body fixes the
// whole class at once, and makes any column added to model.AddressBook in the
// future read-only for admin edits until it is listed here on purpose.
//
// Deliberately absent: row_id (the key being edited, never a payload), pinned
// (owned exclusively by the classification pin endpoint), created_at and
// updated_at (owned by GORM).
var adminEditableColumns = map[string]string{
	"id":               "id",
	"username":         "username",
	"password":         "password",
	"hostname":         "hostname",
	"alias":            "alias",
	"platform":         "platform",
	"tags":             "tags",
	"hash":             "hash",
	"user_id":          "user_id",
	"forceAlwaysRelay": "force_always_relay",
	"rdpPort":          "rdp_port",
	"rdpUsername":      "rdp_username",
	"online":           "online",
	"loginName":        "login_name",
	"sameServer":       "same_server",
	"collection_id":    "collection_id",
}

// BindAddressBookUpdate decodes an administrative address-book edit from the
// raw request body, returning both the typed form and the exact set of columns
// the request addresses.
//
// The body is decoded twice on purpose: once into the form, for correctly typed
// values, and once into a key set, to learn what the caller actually sent.
// Columns the caller omitted are left out of the write-set entirely and keep
// their stored value. Unknown keys are ignored rather than rejected, matching
// how the endpoint behaved before.
func BindAddressBookUpdate(raw []byte) (*AddressBookForm, []string, error) {
	f := &AddressBookForm{}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, nil, err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return nil, nil, err
	}
	cols := make([]string, 0, len(present))
	for key := range present {
		if col, ok := adminEditableColumns[key]; ok {
			cols = append(cols, col)
		}
	}
	// Deterministic order keeps generated SQL and test assertions stable.
	sort.Strings(cols)
	return f, cols, nil
}

type AddressBookQuery struct {
	UserId       int    `form:"user_id"`
	CollectionId *int   `form:"collection_id"`
	IsMy         int    `form:"is_my"`
	Username     string `form:"username"`
	Hostname     string `form:"hostname"`
	Id           string `form:"id"`
	PageQuery
}

type ShareByWebClientForm struct {
	Id           string `json:"id" validate:"required"`
	PasswordType string `json:"password_type" validate:"required,oneof=once fixed"` //只能是once,fixed
	Password     string `json:"password" validate:"required"`
	Expire       int64  `json:"expire"`
}

func (sbwcf ShareByWebClientForm) ToShareRecord() *model.ShareRecord {
	return &model.ShareRecord{
		UserId:       0,
		PeerId:       sbwcf.Id,
		PasswordType: sbwcf.PasswordType,
		Password:     sbwcf.Password,
		Expire:       sbwcf.Expire,
	}
}

type AddressBookCollectionQuery struct {
	UserId int `form:"user_id"`
	IsMy   int `form:"is_my"`
	PageQuery
}

type AddressBookCollectionSimpleListQuery struct {
	UserIds []uint `form:"user_ids"`
}
type AddressBookCollectionRuleQuery struct {
	UserId       int `form:"user_id"`
	CollectionId int `form:"collection_id"`
	IsMy         int `form:"is_my"`
	PageQuery
}

type BatchCreateFromPeersForm struct {
	CollectionId uint     `json:"collection_id"`
	PeerIds      []uint   `json:"peer_ids"`
	Tags         []string `json:"tags"`
	UserId       uint     `json:"user_id"`
}
type BatchUpdateTagsForm struct {
	RowIds []uint   `json:"row_ids"`
	Tags   []string `json:"tags"`
}

// BatchSetPasswordForm assigns one peer password to many address-book entries.
//
// The target set is addressed EITHER by explicit row ids OR by collection —
// exactly one of the two, never both and never neither, so the request can
// never be ambiguous about what it is about to overwrite.
//
// UserId is an optional scope guard, not the target selector. When present,
// every entry outside that user is refused (reported as "forbidden") instead of
// being written. It is optional because the same physical machine legitimately
// appears in several users' address books, and its password is a property of
// the machine, not of the user — refusing cross-user batches outright would
// defeat the point of assigning at scale. Callers that DO want a single-user
// batch can pin it by sending user_id.
type BatchSetPasswordForm struct {
	RowIds       []uint `json:"row_ids"`
	CollectionId uint   `json:"collection_id"`
	UserId       uint   `json:"user_id"`
	Password     string `json:"password" validate:"required"`
}
