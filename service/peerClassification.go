package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/model/custom_types"
	"gorm.io/gorm"
)

// Undo failure reasons (their strings double as i18n keys).
var (
	ErrNoRun           = errors.New("PeerClassificationNoRun")
	ErrAlreadyReverted = errors.New("PeerClassificationAlreadyReverted")
)

// PeerClassificationService owns the rule-matching engine plus CRUD and
// ambiguity validation for PeerClassificationRule. Methods use the package-level
// DB, so a nil receiver is safe (same convention as the other services).
type PeerClassificationService struct{}

// ───────────────────────────── Matchers ─────────────────────────────
//
// Matching is case-insensitive (hostnames are case-insensitive by convention;
// peers.hostname stores whatever the client reported, which may vary in case).
// This is a deliberate design decision — see BUILD_LOG 2026-07-23.

func isAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// containsToken reports whether pattern occurs in hostname as a whole token,
// i.e. bounded on both sides by a non-alphanumeric character or a string end.
// Both arguments are expected already lower-cased. This is what makes
// "contains suc47" match "svr-suc47" but NOT "suc470" (the trailing '0' is
// alphanumeric, so suc47 is not a complete token there).
func containsToken(hostname, pattern string) bool {
	if pattern == "" {
		return false
	}
	from := 0
	for from <= len(hostname)-len(pattern) {
		i := strings.Index(hostname[from:], pattern)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(pattern)
		beforeOK := start == 0 || !isAlnum(hostname[start-1])
		afterOK := end == len(hostname) || !isAlnum(hostname[end])
		if beforeOK && afterOK {
			return true
		}
		from = start + 1
	}
	return false
}

// MatcherMatches reports whether hostname matches the (matcherType, pattern)
// rule. Empty pattern never matches.
func MatcherMatches(matcherType, pattern, hostname string) bool {
	if pattern == "" {
		return false
	}
	h := strings.ToLower(hostname)
	p := strings.ToLower(pattern)
	switch matcherType {
	case model.MatcherTypePrefix:
		return strings.HasPrefix(h, p)
	case model.MatcherTypeSuffix:
		return strings.HasSuffix(h, p)
	case model.MatcherTypeExact:
		return h == p
	case model.MatcherTypeContains:
		return containsToken(h, p)
	default:
		return false
	}
}

// ───────────────────────────── Evaluation ─────────────────────────────

// ClassificationOutcome is the resolved classification for a single hostname
// against a set of rules.
type ClassificationOutcome struct {
	// Matched is true if at least one active rule matched.
	Matched bool
	// TargetCollectionId is the collection resolved by the highest-priority
	// matching rule that specifies one; nil means no rule routed to a collection.
	TargetCollectionId *uint
	// DecidedByRuleId is the id of the rule that decided the collection (0 if none).
	DecidedByRuleId uint
	// Tags is the ordered union of tags contributed by every matching rule.
	Tags []string
}

// Evaluate resolves the outcome for hostname against rules. Rules are evaluated
// in deterministic order: priority descending, then id ascending. The collection
// is decided by the first (highest-priority) matching rule that carries a
// non-nil TargetCollectionId; tags are the union across all matching rules
// (tags never compete). Inactive rules are ignored.
func (s *PeerClassificationService) Evaluate(hostname string, rules []*model.PeerClassificationRule) ClassificationOutcome {
	out := ClassificationOutcome{}

	sorted := make([]*model.PeerClassificationRule, 0, len(rules))
	for _, r := range rules {
		if r != nil && r.Active {
			sorted = append(sorted, r)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].Id < sorted[j].Id
	})

	seen := make(map[string]bool)
	for _, r := range sorted {
		if !MatcherMatches(r.MatcherType, r.Pattern, hostname) {
			continue
		}
		out.Matched = true
		if out.TargetCollectionId == nil && r.TargetCollectionId != nil {
			cid := *r.TargetCollectionId
			out.TargetCollectionId = &cid
			out.DecidedByRuleId = r.Id
		}
		for _, t := range DecodeTags(r.TargetTags) {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out.Tags = append(out.Tags, t)
		}
	}
	return out
}

// DecodeTags parses an AutoJson tag array into a []string, tolerating empty/nil.
func DecodeTags(raw custom_types.AutoJson) []string {
	if len(raw) == 0 {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return tags
}

// EncodeTags serialises a []string into an AutoJson value (never nil-JSON).
func EncodeTags(tags []string) custom_types.AutoJson {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return custom_types.AutoJson(b)
}

// ───────────────────────────── Ambiguity ─────────────────────────────

// patternsCanOverlapForCollision reports whether two SAME-priority collection
// rules should be treated as colliding — i.e. whether they could plausibly route
// the same real device to two different collections.
//
// This is deliberately NOT the literal "could any hypothetical hostname match
// both" question. Under token semantics almost any two `contains` tokens can be
// forced to co-occur in a synthetic hostname (e.g. "svr-suc01-suc02"), so the
// literal answer for contains-vs-contains is always yes. But disjoint branch
// tokens like "suc01" and "suc02" never target the same real device, so flagging
// them as a conflict is a false positive that blocks a legitimate setup (54
// branches suc01..suc54 sharing one priority). The collision criterion is
// therefore substring-based:
//
//	exact    vs exact    → collide iff identical
//	prefix   vs prefix   → collide iff one is a prefix of the other
//	suffix   vs suffix   → collide iff one is a suffix of the other
//	contains vs contains → collide iff one is a substring of the other
//	any pair with exact  → collide iff the exact literal satisfies the other matcher
//	mixed anchors        → collide (prefix/suffix, prefix/contains, suffix/contains
//	                        can all be satisfied by one real hostname, e.g. prefix
//	                        "svr-" and contains "suc01" both match "svr-suc01")
//
// Substring/prefix/suffix pairs are genuine collisions: if A is a substring of B
// then every device the more specific rule catches is also caught by the broader
// one, so with equal priority the target collection is ambiguous.
func patternsCanOverlapForCollision(aType, aPattern, bType, bPattern string) bool {
	ap := strings.ToLower(aPattern)
	bp := strings.ToLower(bPattern)
	if ap == "" || bp == "" {
		return false
	}

	// Any pair involving an exact matcher: collide iff the exact literal itself
	// satisfies the other matcher (this yields exact-vs-exact = identical).
	if aType == model.MatcherTypeExact && bType == model.MatcherTypeExact {
		return ap == bp
	}
	if aType == model.MatcherTypeExact {
		return MatcherMatches(bType, bp, ap)
	}
	if bType == model.MatcherTypeExact {
		return MatcherMatches(aType, ap, bp)
	}

	// Same-type anchors collide only when one pattern contains the other in the
	// relevant position. Disjoint patterns (neither contained in the other) do
	// not collide.
	switch {
	case aType == model.MatcherTypePrefix && bType == model.MatcherTypePrefix:
		return strings.HasPrefix(ap, bp) || strings.HasPrefix(bp, ap)
	case aType == model.MatcherTypeSuffix && bType == model.MatcherTypeSuffix:
		return strings.HasSuffix(ap, bp) || strings.HasSuffix(bp, ap)
	case aType == model.MatcherTypeContains && bType == model.MatcherTypeContains:
		return strings.Contains(ap, bp) || strings.Contains(bp, ap)
	default:
		// Mixed anchors: a single real hostname can satisfy both, so keep
		// treating them as a real overlap.
		return true
	}
}

// ───────────────────────────── CRUD ─────────────────────────────

func (s *PeerClassificationService) InfoById(id uint) *model.PeerClassificationRule {
	r := &model.PeerClassificationRule{}
	DB.Where("id = ?", id).First(r)
	return r
}

func (s *PeerClassificationService) ListRules(page, pageSize uint, where func(tx *gorm.DB)) *model.PeerClassificationRuleList {
	res := &model.PeerClassificationRuleList{}
	res.Page = int64(page)
	res.PageSize = int64(pageSize)
	tx := DB.Model(&model.PeerClassificationRule{})
	if where != nil {
		where(tx)
	}
	tx.Count(&res.Total)
	tx.Scopes(Paginate(page, pageSize)).Order("priority desc, id asc").Find(&res.PeerClassificationRules)
	return res
}

// ActiveRulesByUserId returns all active rules for a user, ordered by priority
// desc then id asc (ready for Evaluate, though Evaluate re-sorts defensively).
func (s *PeerClassificationService) ActiveRulesByUserId(userId uint) []*model.PeerClassificationRule {
	var rules []*model.PeerClassificationRule
	DB.Where("user_id = ? AND active = ?", userId, true).
		Order("priority desc, id asc").Find(&rules)
	return rules
}

func (s *PeerClassificationService) CreateRule(r *model.PeerClassificationRule) error {
	return DB.Create(r).Error
}

func (s *PeerClassificationService) UpdateRule(r *model.PeerClassificationRule) error {
	// Select("*") so that clearing target_collection_id / toggling active to
	// false is persisted (zero-values would otherwise be skipped by Updates).
	return DB.Model(r).Select(
		"user_id", "matcher_type", "pattern", "target_collection_id",
		"target_tags", "priority", "active",
	).Updates(r).Error
}

func (s *PeerClassificationService) DeleteRule(r *model.PeerClassificationRule) error {
	return DB.Delete(r).Error
}

// FindDuplicate returns an existing rule with the same (user_id, matcher_type,
// pattern) as the given one, excluding excludeId (0 = exclude nothing). Used to
// hard-reject exact duplicates. Comparison is case-insensitive on pattern to
// match the matcher semantics.
func (s *PeerClassificationService) FindDuplicate(userId uint, matcherType, pattern string, excludeId uint) *model.PeerClassificationRule {
	var r model.PeerClassificationRule
	tx := DB.Where("user_id = ? AND matcher_type = ? AND LOWER(pattern) = LOWER(?)", userId, matcherType, pattern)
	if excludeId > 0 {
		tx = tx.Where("id <> ?", excludeId)
	}
	if err := tx.First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// FindConflict returns another active rule owned by the same user, at the SAME
// priority, that routes to a DIFFERENT collection and whose matcher can overlap
// with r's matcher — i.e. a genuine same-priority collection ambiguity. Returns
// nil when r is inactive or has no target collection (nothing to conflict over).
func (s *PeerClassificationService) FindConflict(r *model.PeerClassificationRule) *model.PeerClassificationRule {
	if !r.Active || r.TargetCollectionId == nil {
		return nil
	}
	var others []*model.PeerClassificationRule
	tx := DB.Where("user_id = ? AND priority = ? AND active = ? AND target_collection_id IS NOT NULL",
		r.UserId, r.Priority, true)
	if r.Id > 0 {
		tx = tx.Where("id <> ?", r.Id)
	}
	tx.Find(&others)
	for _, o := range others {
		if o.TargetCollectionId == nil || *o.TargetCollectionId == *r.TargetCollectionId {
			continue
		}
		if patternsCanOverlapForCollision(r.MatcherType, r.Pattern, o.MatcherType, o.Pattern) {
			return o
		}
	}
	return nil
}

// ───────────────────────────── Plan / Apply ─────────────────────────────

// Classification action verbs for a single peer.
const (
	PCActionCreate    = "create"    // peer not in AB, will be inserted into resolved collection
	PCActionMove      = "move"      // existing AB entry changes collection
	PCActionUpdate    = "update"    // existing AB entry, same collection, alias/tags change
	PCActionUnchanged = "unchanged" // matched a collection but everything already correct
	PCActionNone      = "none"      // no collection rule matched → peer left untouched
	PCActionPinned    = "pinned"    // matched a collection rule but the entry is pinned → protected
)

// PeerClassificationResult is the per-peer outcome shared by /simulate and /apply.
type PeerClassificationResult struct {
	PeerRowId            uint     `json:"peer_id"` // peers.row_id
	PeerId               string   `json:"id"`      // peers.id (rustdesk id)
	Hostname             string   `json:"hostname"`
	CurrentCollectionId  *uint    `json:"current_collection_id"`
	ProposedCollectionId *uint    `json:"proposed_collection_id"`
	CurrentTags          []string `json:"current_tags"`
	ProposedTags         []string `json:"proposed_tags"`
	CurrentAlias         string   `json:"current_alias"`
	ProposedAlias        string   `json:"proposed_alias"`
	Action               string   `json:"action"`
	Changes              bool     `json:"changes"`
	// Pinned reflects the entry's pinned flag. When true and a rule matched, the
	// proposed_* fields still show what the rule WOULD have done, but Changes is
	// false and the entry is left untouched.
	Pinned bool `json:"pinned"`
}

// PlanSummary aggregates a plan/apply run.
type PlanSummary struct {
	Total         int `json:"total"`
	Created       int `json:"created"`
	Moved         int `json:"moved"`
	Updated       int `json:"updated"`
	Unchanged     int `json:"unchanged"`
	NoMatch       int `json:"no_match"`
	PinnedSkipped int `json:"pinned_skipped"`
}

// currentABEntry returns the (single) address-book entry that this system
// manages for a peer, keyed by (user_id, peer_id); nil if the peer is not yet in
// the address book. If the admin manually created the same peer in several
// collections, only the first is considered (documented limitation).
func currentABEntry(userId uint, peerId string) *model.AddressBook {
	ab := &model.AddressBook{}
	if err := DB.Where("user_id = ? AND id = ?", userId, peerId).First(ab).Error; err != nil {
		return nil
	}
	return ab
}

func unionTags(current, add []string) []string {
	out := make([]string, 0, len(current)+len(add))
	seen := make(map[string]bool)
	for _, t := range current {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range add {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// BuildPlan computes, read-only, the classification plan for all of userId's
// peers against their active rules. It performs NO writes. Evaluation runs
// against peers.hostname (the live value), never address_books.hostname.
func (s *PeerClassificationService) BuildPlan(userId uint) ([]PeerClassificationResult, PlanSummary) {
	rules := s.ActiveRulesByUserId(userId)

	// Evaluate ALL peers, not a user_id-scoped subset. In Lejianwen peers.user_id
	// is 0 for every peer (including real production devices reported by the
	// RustDesk client) — the column exists but is not used to assign device
	// ownership. Filtering by it made /simulate return Total 0 against the real
	// lab DB. userId still scopes the RULES (above) and the address_books entries
	// that get created/updated (currentABEntry / the writer), where it IS real.
	var peers []*model.Peer
	DB.Order("row_id asc").Find(&peers)

	results := make([]PeerClassificationResult, 0, len(peers))
	summary := PlanSummary{Total: len(peers)}

	for _, p := range peers {
		outcome := s.Evaluate(p.Hostname, rules)
		ab := currentABEntry(userId, p.Id)

		res := PeerClassificationResult{
			PeerRowId: p.RowId,
			PeerId:    p.Id,
			Hostname:  p.Hostname,
		}
		if ab != nil {
			cid := ab.CollectionId
			res.CurrentCollectionId = &cid
			res.CurrentTags = DecodeTags(ab.Tags)
			res.CurrentAlias = ab.Alias
			res.Pinned = ab.Pinned
		}

		if outcome.TargetCollectionId == nil {
			// No collection rule matched → never touched (not even alias/tags).
			res.Action = PCActionNone
			res.ProposedCollectionId = res.CurrentCollectionId
			res.ProposedTags = res.CurrentTags
			res.ProposedAlias = res.CurrentAlias
			summary.NoMatch++
			results = append(results, res)
			continue
		}

		res.ProposedCollectionId = outcome.TargetCollectionId
		res.ProposedAlias = p.Hostname // alias mirrors the live hostname
		res.ProposedTags = unionTags(res.CurrentTags, outcome.Tags)

		// Pinned entries are protected: a rule matched, and the proposed_* fields
		// above show what it WOULD have done, but the entry is left untouched.
		if res.Pinned {
			res.Action = PCActionPinned
			res.Changes = false
			summary.PinnedSkipped++
			results = append(results, res)
			continue
		}

		switch {
		case ab == nil:
			res.Action = PCActionCreate
			res.Changes = true
			summary.Created++
		case *res.CurrentCollectionId != *outcome.TargetCollectionId:
			res.Action = PCActionMove
			res.Changes = true
			summary.Moved++
		case res.CurrentAlias != p.Hostname || len(res.ProposedTags) != len(res.CurrentTags):
			res.Action = PCActionUpdate
			res.Changes = true
			summary.Updated++
		default:
			res.Action = PCActionUnchanged
			summary.Unchanged++
		}
		results = append(results, res)
	}
	return results, summary
}

// pcWriteOne persists a single planned change within a transaction. It is a
// package variable so tests can inject a fault to verify full rollback (there is
// no natural constraint to violate mid-batch on SQLite). Production always uses
// pcWriteOneDefault.
var pcWriteOne = pcWriteOneDefault

func pcWriteOneDefault(tx *gorm.DB, userId uint, r *PeerClassificationResult, peer *model.Peer) error {
	switch r.Action {
	case PCActionCreate:
		ab := &model.AddressBook{
			Id:           peer.Id,
			Username:     peer.Username,
			Hostname:     peer.Hostname,
			Alias:        peer.Hostname, // mirror, always
			Platform:     AllService.AddressBookService.PlatformFromOs(peer.Os),
			UserId:       userId,
			CollectionId: *r.ProposedCollectionId,
			Tags:         EncodeTags(r.ProposedTags),
		}
		return tx.Create(ab).Error
	case PCActionMove, PCActionUpdate:
		return tx.Model(&model.AddressBook{}).
			Where("user_id = ? AND id = ?", userId, peer.Id).
			Updates(map[string]interface{}{
				"collection_id": *r.ProposedCollectionId,
				"alias":         peer.Hostname,
				"tags":          EncodeTags(r.ProposedTags),
			}).Error
	default:
		return nil
	}
}

// Apply computes the plan and executes every change inside a single transaction
// (all-or-nothing). On any write error the whole batch rolls back and the error
// is returned with no partial changes. adminId is the executing user; userId is
// whose peers are classified (equal in the current /me-scoped controller).
//
// The audit run AND its per-entry snapshot are written INSIDE the same
// transaction (not best-effort as before): undo depends on the snapshot being
// atomic with the changes it describes.
func (s *PeerClassificationService) Apply(userId, adminId uint) ([]PeerClassificationResult, PlanSummary, error) {
	results, summary := s.BuildPlan(userId)

	// Index peers by rustdesk id for the writer (need Username/Os on create).
	// Same as BuildPlan: evaluate ALL peers (peers.user_id is 0 in Lejianwen).
	var peers []*model.Peer
	DB.Find(&peers)
	peerById := make(map[string]*model.Peer, len(peers))
	for _, p := range peers {
		peerById[p.Id] = p
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		items := make([]*model.AuditPeerClassificationItem, 0)
		for i := range results {
			r := &results[i]
			if !r.Changes {
				continue
			}
			peer := peerById[r.PeerId]

			item := &model.AuditPeerClassificationItem{
				PeerId:              r.PeerId,
				Operation:           r.Action,
				AppliedCollectionId: *r.ProposedCollectionId,
				AppliedAlias:        peer.Hostname,
				AppliedTags:         EncodeTags(r.ProposedTags),
			}
			// For moved/updated, snapshot the previous state before overwriting.
			if r.Action != PCActionCreate {
				var prev model.AddressBook
				if err := tx.Where("user_id = ? AND id = ?", userId, r.PeerId).First(&prev).Error; err != nil {
					return err
				}
				item.RowId = prev.RowId
				item.PrevCollectionId = prev.CollectionId
				item.PrevAlias = prev.Alias
				item.PrevTags = normalizeTags(prev.Tags)
			}

			if err := pcWriteOne(tx, userId, r, peer); err != nil {
				return err
			}

			// For created rows, capture the freshly assigned row_id.
			if r.Action == PCActionCreate {
				var created model.AddressBook
				if err := tx.Where("user_id = ? AND id = ?", userId, r.PeerId).First(&created).Error; err != nil {
					return err
				}
				item.RowId = created.RowId
			}
			items = append(items, item)
		}

		audit := &model.AuditPeerClassification{
			Kind:          model.AuditPCKindApply,
			AdminId:       adminId,
			UserId:        userId,
			Total:         summary.Total,
			Created:       summary.Created,
			Moved:         summary.Moved,
			Updated:       summary.Updated,
			NoMatch:       summary.NoMatch,
			PinnedSkipped: summary.PinnedSkipped,
		}
		if err := tx.Create(audit).Error; err != nil {
			return err
		}
		for _, it := range items {
			it.AuditId = audit.Id
			if err := tx.Create(it).Error; err != nil {
				return err
			}
		}

		// Retention: only the latest apply run is reversible, so the per-entry
		// snapshot of this user's PREVIOUS apply runs is no longer useful. Purge
		// that detail while keeping the summary audit rows for history.
		var priorRunIds []uint
		tx.Model(&model.AuditPeerClassification{}).
			Where("user_id = ? AND kind = ? AND id <> ?", userId, model.AuditPCKindApply, audit.Id).
			Pluck("id", &priorRunIds)
		if len(priorRunIds) > 0 {
			if err := tx.Where("audit_id IN ?", priorRunIds).
				Delete(&model.AuditPeerClassificationItem{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, PlanSummary{}, err
	}

	return results, summary, nil
}

// normalizeTags returns a canonical JSON array for a tag blob (defaults empty
// to "[]"), so snapshots compare cleanly.
func normalizeTags(raw custom_types.AutoJson) custom_types.AutoJson {
	return EncodeTags(DecodeTags(raw))
}

// CreateApplyAudit persists one audit row for an apply run.
func (s *PeerClassificationService) CreateApplyAudit(a *model.AuditPeerClassification) error {
	return DB.Create(a).Error
}

// SetPinned sets/clears the pinned flag on the address-book entry with rowId.
func (s *PeerClassificationService) SetPinned(rowId uint, pinned bool) error {
	return DB.Model(&model.AddressBook{}).Where("row_id = ?", rowId).Update("pinned", pinned).Error
}

// ───────────────────────────── Undo ─────────────────────────────

// LastRunInfo describes the most recent apply run of a user and whether it can
// still be undone.
type LastRunInfo struct {
	Run        *model.AuditPeerClassification
	Reversible bool
	Reason     string // "" if reversible; "no_runs" or "already_reverted" otherwise
}

// LastRun returns the user's most recent apply run and its reversibility. Only
// the single most recent apply run is ever undoable (design decision): undoing
// an older run with newer runs on top would produce inconsistent state.
func (s *PeerClassificationService) LastRun(userId uint) LastRunInfo {
	var run model.AuditPeerClassification
	err := DB.Where("user_id = ? AND kind = ?", userId, model.AuditPCKindApply).
		Order("id desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LastRunInfo{Run: nil, Reversible: false, Reason: "no_runs"}
	}
	if err != nil {
		return LastRunInfo{Run: nil, Reversible: false, Reason: "no_runs"}
	}
	if run.RevertedByAuditId != 0 {
		return LastRunInfo{Run: &run, Reversible: false, Reason: "already_reverted"}
	}
	return LastRunInfo{Run: &run, Reversible: true, Reason: ""}
}

// UndoResult summarises an undo run.
type UndoResult struct {
	RevertedAuditId uint `json:"reverted_audit_id"`
	Deleted         int  `json:"deleted"`          // created entries removed
	Restored        int  `json:"restored"`         // moved/updated entries restored
	SkippedModified int  `json:"skipped_modified"` // changed after apply, left alone
	SkippedPinned   int  `json:"skipped_pinned"`   // pinned after apply, left alone
}

// tagsSetEqual reports set (multiset) equality of two tag slices.
func tagsSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, t := range a {
		m[t]++
	}
	for _, t := range b {
		m[t]--
		if m[t] < 0 {
			return false
		}
	}
	return true
}

// entryMatchesApplied reports whether an entry is still exactly as apply left it
// (collection, alias and tag set). If not, a human changed it after the apply.
func entryMatchesApplied(ab *model.AddressBook, item *model.AuditPeerClassificationItem) bool {
	return ab.CollectionId == item.AppliedCollectionId &&
		ab.Alias == item.AppliedAlias &&
		tagsSetEqual(DecodeTags(ab.Tags), DecodeTags(item.AppliedTags))
}

// pcUndoOne performs the actual DB mutation for one snapshot item. Package var so
// tests can inject a fault to verify full rollback.
var pcUndoOne = pcUndoOneDefault

func pcUndoOneDefault(tx *gorm.DB, item *model.AuditPeerClassificationItem) error {
	if item.Operation == PCActionCreate {
		return tx.Where("row_id = ?", item.RowId).Delete(&model.AddressBook{}).Error
	}
	return tx.Model(&model.AddressBook{}).Where("row_id = ?", item.RowId).
		Updates(map[string]interface{}{
			"collection_id": item.PrevCollectionId,
			"alias":         item.PrevAlias,
			"tags":          normalizeTags(item.PrevTags),
		}).Error
}

// Undo reverts the user's most recent apply run, inside a single transaction:
// created entries are deleted; moved/updated entries are restored to their
// snapshot. Entries changed (or pinned) after the apply are left untouched and
// reported. The undo is recorded as its own audit event and the original run is
// marked reverted (so it cannot be undone again — there is no redo).
func (s *PeerClassificationService) Undo(userId, adminId uint) (*UndoResult, error) {
	info := s.LastRun(userId)
	if info.Run == nil {
		return nil, ErrNoRun
	}
	if !info.Reversible {
		return nil, ErrAlreadyReverted
	}
	run := info.Run

	var items []*model.AuditPeerClassificationItem
	DB.Where("audit_id = ?", run.Id).Find(&items)

	res := &UndoResult{RevertedAuditId: run.Id}
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			var ab model.AddressBook
			err := tx.Where("row_id = ?", item.RowId).First(&ab).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Row deleted externally after apply → treat as modified.
				res.SkippedModified++
				continue
			}
			if err != nil {
				return err
			}
			if ab.Pinned {
				res.SkippedPinned++
				continue
			}
			if !entryMatchesApplied(&ab, item) {
				res.SkippedModified++
				continue
			}
			if err := pcUndoOne(tx, item); err != nil {
				return err
			}
			if item.Operation == PCActionCreate {
				res.Deleted++
			} else {
				res.Restored++
			}
		}

		undoEvent := &model.AuditPeerClassification{
			Kind:            model.AuditPCKindUndo,
			AdminId:         adminId,
			UserId:          userId,
			RevertedAuditId: run.Id,
			Deleted:         res.Deleted,
			Restored:        res.Restored,
			SkippedModified: res.SkippedModified,
			SkippedPinned:   res.SkippedPinned,
		}
		if err := tx.Create(undoEvent).Error; err != nil {
			return err
		}
		// Mark the original run reverted so it cannot be undone again.
		return tx.Model(&model.AuditPeerClassification{}).Where("id = ?", run.Id).
			Update("reverted_by_audit_id", undoEvent.Id).Error
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
