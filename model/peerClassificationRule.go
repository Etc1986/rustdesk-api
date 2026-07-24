package model

import "github.com/lejianwen/rustdesk-api/v2/model/custom_types"

// Matcher types for PeerClassificationRule.Pattern evaluation against a peer's
// live hostname. See service/peerClassification.go for the matching semantics.
const (
	MatcherTypePrefix   = "prefix"
	MatcherTypeSuffix   = "suffix"
	MatcherTypeContains = "contains"
	MatcherTypeExact    = "exact"
)

// PeerClassificationRule is a user-owned rule that, when its matcher matches a
// peer's *live* hostname (peers.hostname — NOT the frozen address_books.hostname,
// see docs/investigacion/sync-hostname-address-book.md), routes that peer into a
// target address-book collection and/or attaches tags.
//
// This is deliberately a DIFFERENT model from AddressBookCollectionRule, which is
// an unrelated address-book *sharing/ACL* model
// (see docs/investigacion/address-book-collection-rule-diagnostico.md). They must
// not be confused or reused for one another.
type PeerClassificationRule struct {
	IdModel
	// UserId is the owner of the rule; all evaluation is scoped to this user's
	// own peers and collections (multi-tenant, same pattern as the /my variant
	// of AddressBookCollectionRule).
	UserId uint `json:"user_id" gorm:"default:0;not null;index"`
	// MatcherType is one of prefix|suffix|contains|exact.
	MatcherType string `json:"matcher_type" gorm:"default:'';not null;" validate:"required,oneof=prefix suffix contains exact"`
	// Pattern is the text matched against the peer hostname (case-insensitive).
	Pattern string `json:"pattern" gorm:"default:'';not null;" validate:"required"`
	// TargetCollectionId is the destination collection; nil/absent means the rule
	// only contributes tags and never resolves a collection.
	TargetCollectionId *uint `json:"target_collection_id" gorm:"index"`
	// TargetTags is a JSON array of tag strings to add (union) when the rule matches.
	TargetTags custom_types.AutoJson `json:"target_tags" gorm:"not null;" swaggertype:"array,string"`
	// Priority: higher number is evaluated first and wins collection ties.
	Priority int `json:"priority" gorm:"default:0;not null;index"`
	// Active gates whether the rule participates in evaluation.
	Active bool `json:"active" gorm:"default:1;not null;"`
	TimeModel
}

type PeerClassificationRuleList struct {
	PeerClassificationRules []*PeerClassificationRule `json:"list"`
	Pagination
}

// Audit event kinds.
const (
	AuditPCKindApply = "apply"
	AuditPCKindUndo  = "undo"
)

// AuditPeerClassification records one classification event: either an "apply"
// run or an "undo" of a previous apply. One row per event; counters summarise
// the outcome. Mirrors the AuditAbBatch pattern.
type AuditPeerClassification struct {
	IdModel
	// Kind is "apply" or "undo".
	Kind    string `json:"kind" gorm:"default:'apply';not null;index"`
	AdminId uint   `json:"admin_id" gorm:"default:0;not null;index"` // who executed the event
	UserId  uint   `json:"user_id" gorm:"default:0;not null;index"`  // whose peers were evaluated
	// Apply-event counters.
	Total         int `json:"total" gorm:"default:0;not null"`          // peers evaluated
	Created       int `json:"created" gorm:"default:0;not null"`        // AB entries newly created
	Moved         int `json:"moved" gorm:"default:0;not null"`          // entries whose collection changed
	Updated       int `json:"updated" gorm:"default:0;not null"`        // entries touched (alias/tags) without a move
	NoMatch       int `json:"no_match" gorm:"default:0;not null"`       // peers matching no collection rule (untouched)
	PinnedSkipped int `json:"pinned_skipped" gorm:"default:0;not null"` // pinned entries left untouched despite a match
	// Reversibility links.
	RevertedByAuditId uint `json:"reverted_by_audit_id" gorm:"default:0;not null;index"` // apply-event: id of the undo that reverted it (0 = not reverted)
	RevertedAuditId   uint `json:"reverted_audit_id" gorm:"default:0;not null;index"`    // undo-event: id of the apply it reverted (0 for apply)
	// Undo-event counters.
	Deleted         int `json:"deleted" gorm:"default:0;not null"`          // created entries removed
	Restored        int `json:"restored" gorm:"default:0;not null"`         // moved/updated entries restored
	SkippedModified int `json:"skipped_modified" gorm:"default:0;not null"` // entries changed after apply, left alone
	SkippedPinned   int `json:"skipped_pinned" gorm:"default:0;not null"`   // entries pinned after apply, left alone
	TimeModel
}

type AuditPeerClassificationList struct {
	AuditPeerClassifications []*AuditPeerClassification `json:"list"`
	Pagination
}

// AuditPeerClassificationItem is the per-entry snapshot captured during an apply
// run, one row per touched address-book entry. It stores both the PREVIOUS state
// (to restore on undo) and the APPLIED state (to detect whether a human changed
// the entry afterwards, in which case undo must skip it).
type AuditPeerClassificationItem struct {
	IdModel
	AuditId   uint   `json:"audit_id" gorm:"default:0;not null;index"` // the apply run this snapshot belongs to
	RowId     uint   `json:"row_id" gorm:"default:0;not null;index"`   // address_books.row_id
	PeerId    string `json:"peer_id" gorm:"default:'';not null;"`      // rustdesk peer id (peers.id)
	Operation string `json:"operation" gorm:"default:'';not null;"`    // created | moved | updated
	// Previous state (before apply); empty/zero for "created".
	PrevCollectionId uint                  `json:"prev_collection_id" gorm:"default:0;not null;"`
	PrevAlias        string                `json:"prev_alias" gorm:"default:'';not null;"`
	PrevTags         custom_types.AutoJson `json:"prev_tags" gorm:"not null;" swaggertype:"array,string"`
	// State apply left the entry in (to detect later manual edits).
	AppliedCollectionId uint                  `json:"applied_collection_id" gorm:"default:0;not null;"`
	AppliedAlias        string                `json:"applied_alias" gorm:"default:'';not null;"`
	AppliedTags         custom_types.AutoJson `json:"applied_tags" gorm:"not null;" swaggertype:"array,string"`
	TimeModel
}
