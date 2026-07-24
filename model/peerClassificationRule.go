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

// AuditPeerClassification records one invocation of the classification "apply"
// operation. One row per apply run; counters summarise the outcome. Mirrors the
// AuditAbBatch pattern.
type AuditPeerClassification struct {
	IdModel
	AdminId  uint `json:"admin_id" gorm:"default:0;not null;index"` // who executed the run
	UserId   uint `json:"user_id" gorm:"default:0;not null;index"`  // whose peers were evaluated
	Total    int  `json:"total" gorm:"default:0;not null"`          // peers evaluated
	Created  int  `json:"created" gorm:"default:0;not null"`        // AB entries newly created
	Moved    int  `json:"moved" gorm:"default:0;not null"`          // entries whose collection changed
	Updated  int  `json:"updated" gorm:"default:0;not null"`        // entries touched (alias/tags) without a move
	NoMatch  int  `json:"no_match" gorm:"default:0;not null"`       // peers matching no collection rule (untouched)
	TimeModel
}

type AuditPeerClassificationList struct {
	AuditPeerClassifications []*AuditPeerClassification `json:"list"`
	Pagination
}
