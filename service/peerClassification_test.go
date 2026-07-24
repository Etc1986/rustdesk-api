package service

import (
	"testing"

	"github.com/lejianwen/rustdesk-api/v2/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupPCTestDB wires an in-memory SQLite DB with the classification models.
func setupPCTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	if err := db.AutoMigrate(
		&model.PeerClassificationRule{},
		&model.AuditPeerClassification{},
		&model.AddressBook{},
		&model.AddressBookCollection{},
		&model.Peer{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	DB = db
	AllService = &Service{
		AddressBookService:        &AddressBookService{},
		PeerService:               &PeerService{},
		UserService:               &UserService{},
		PeerClassificationService: &PeerClassificationService{},
	}
	return db
}

func uptr(v uint) *uint { return &v }

// ─────────────────────────────────────────────────────────────────────────────
// Test point 1: matchers, incl. the "contains suc47 must not match suc470" case
// ─────────────────────────────────────────────────────────────────────────────

func TestMatcherMatches_Basic(t *testing.T) {
	cases := []struct {
		name     string
		mtype    string
		pattern  string
		hostname string
		want     bool
	}{
		{"prefix hit", model.MatcherTypePrefix, "svr-", "svr-suc47", true},
		{"prefix miss", model.MatcherTypePrefix, "svr-", "wks-suc47", false},
		{"prefix case-insensitive", model.MatcherTypePrefix, "SVR-", "svr-suc47", true},
		{"suffix hit", model.MatcherTypeSuffix, "-c", "svr-suc47-c", true},
		{"suffix miss", model.MatcherTypeSuffix, "-c", "svr-suc47-s", false},
		{"exact hit", model.MatcherTypeExact, "svr-suc47", "svr-suc47", true},
		{"exact case-insensitive", model.MatcherTypeExact, "SVR-SUC47", "svr-suc47", true},
		{"exact miss (substring)", model.MatcherTypeExact, "svr-suc47", "svr-suc470", false},
		{"contains token hit", model.MatcherTypeContains, "suc47", "svr-suc47-c", true},
		{"contains token hit at end", model.MatcherTypeContains, "suc47", "svr-suc47", true},
		{"contains token hit at start", model.MatcherTypeContains, "suc47", "suc47-wks", true},
		{"contains underscore separator", model.MatcherTypeContains, "suc47", "svr_suc47_c", true},
		{"contains dot separator", model.MatcherTypeContains, "suc47", "svr.suc47.lan", true},
		// The critical false-positive guard: suc47 must NOT match suc470.
		{"contains NOT partial number suc470", model.MatcherTypeContains, "suc47", "svr-suc470", false},
		{"contains NOT partial number suc470 bare", model.MatcherTypeContains, "suc47", "suc470", false},
		{"contains NOT glued prefix", model.MatcherTypeContains, "suc47", "xsuc47", false},
		{"empty pattern never matches", model.MatcherTypeContains, "", "anything", false},
		{"unknown matcher type", "regex", "suc47", "suc47", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatcherMatches(c.mtype, c.pattern, c.hostname); got != c.want {
				t.Errorf("MatcherMatches(%q,%q,%q) = %v, want %v", c.mtype, c.pattern, c.hostname, got, c.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 2: priority — specific rule wins over generic when both match
// ─────────────────────────────────────────────────────────────────────────────

func TestEvaluate_PriorityWinsForCollection(t *testing.T) {
	svc := &PeerClassificationService{}

	specific := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(47), Priority: 100, Active: true,
	}
	specific.Id = 1
	generic := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypePrefix, Pattern: "svr-",
		TargetCollectionId: uptr(9), Priority: 10, Active: true,
	}
	generic.Id = 2

	// Order shouldn't matter — pass generic first to prove sorting works.
	out := svc.Evaluate("svr-suc47", []*model.PeerClassificationRule{generic, specific})
	if !out.Matched {
		t.Fatal("expected a match")
	}
	if out.TargetCollectionId == nil || *out.TargetCollectionId != 47 {
		t.Errorf("collection: want 47 (specific), got %v", out.TargetCollectionId)
	}
	if out.DecidedByRuleId != 1 {
		t.Errorf("decided-by: want rule 1 (specific), got %d", out.DecidedByRuleId)
	}
}

func TestEvaluate_TagsUnionAcrossRules(t *testing.T) {
	svc := &PeerClassificationService{}

	colRule := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(47), TargetTags: EncodeTags([]string{"site"}),
		Priority: 50, Active: true,
	}
	colRule.Id = 1
	tagRule := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypeSuffix, Pattern: "-c",
		TargetTags: EncodeTags([]string{"laptop", "site"}), // "site" duplicate → deduped
		Priority:   40, Active: true,
	}
	tagRule.Id = 2

	out := svc.Evaluate("svr-suc47-c", []*model.PeerClassificationRule{colRule, tagRule})
	if out.TargetCollectionId == nil || *out.TargetCollectionId != 47 {
		t.Errorf("collection: want 47, got %v", out.TargetCollectionId)
	}
	// Union, deduped, order-preserving: site, laptop.
	want := []string{"site", "laptop"}
	if len(out.Tags) != len(want) {
		t.Fatalf("tags: want %v, got %v", want, out.Tags)
	}
	for i := range want {
		if out.Tags[i] != want[i] {
			t.Errorf("tags[%d]: want %q, got %q (full %v)", i, want[i], out.Tags[i], out.Tags)
		}
	}
}

func TestEvaluate_InactiveRuleIgnored(t *testing.T) {
	svc := &PeerClassificationService{}
	r := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypeExact, Pattern: "svr-suc47",
		TargetCollectionId: uptr(47), Priority: 100, Active: false,
	}
	r.Id = 1
	out := svc.Evaluate("svr-suc47", []*model.PeerClassificationRule{r})
	if out.Matched {
		t.Error("inactive rule should not match")
	}
}

func TestEvaluate_NoMatch(t *testing.T) {
	svc := &PeerClassificationService{}
	r := &model.PeerClassificationRule{
		MatcherType: model.MatcherTypeContains, Pattern: "suc99",
		TargetCollectionId: uptr(99), Priority: 10, Active: true,
	}
	r.Id = 1
	out := svc.Evaluate("svr-suc47", []*model.PeerClassificationRule{r})
	if out.Matched || out.TargetCollectionId != nil || out.Tags != nil {
		t.Errorf("expected clean no-match, got %+v", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Ambiguity detection: overlap decision + FindConflict / FindDuplicate
// ─────────────────────────────────────────────────────────────────────────────

func TestMatchersCanOverlap(t *testing.T) {
	P, S, C, E := model.MatcherTypePrefix, model.MatcherTypeSuffix, model.MatcherTypeContains, model.MatcherTypeExact
	cases := []struct {
		at, ap, bt, bp string
		want           bool
	}{
		{C, "suc47", P, "svr-", true},        // svr-suc47
		{P, "svr-", S, "-c", true},           // svr-...-c
		{P, "svr-", P, "svr-suc", true},      // one prefix of the other
		{P, "svr-", P, "wks-", false},        // disjoint prefixes
		{S, "-c", S, "x-c", true},            // "-c" is a suffix of "x-c" → some host ends in both
		{S, "-c", S, "-abc", false},          // NOT overlapping: host ending "-abc" ends in "bc", not "-c"
		{S, "-c", S, "-s", false},            // disjoint suffixes
		{E, "svr-suc47", P, "svr-", true},    // exact satisfies prefix
		{E, "svr-suc47", P, "wks-", false},   // exact fails prefix
		{E, "svr-suc47", C, "suc47", true},   // exact contains token
		{E, "svr-suc470", C, "suc47", false}, // exact does NOT contain token suc47
		{E, "a", E, "a", true},               // identical exacts
		{E, "a", E, "b", false},              // different exacts
		{C, "a", C, "b", true},               // contains+contains always constructible
	}
	for _, c := range cases {
		if got := MatchersCanOverlap(c.at, c.ap, c.bt, c.bp); got != c.want {
			t.Errorf("overlap(%s:%q, %s:%q) = %v, want %v", c.at, c.ap, c.bt, c.bp, got, c.want)
		}
	}
}

func TestFindConflict_SamePriorityDifferentCollection(t *testing.T) {
	setupPCTestDB(t)
	svc := &PeerClassificationService{}

	// Existing generic rule: prefix svr- -> collection 9, priority 50.
	existing := &model.PeerClassificationRule{
		UserId: 1, MatcherType: model.MatcherTypePrefix, Pattern: "svr-",
		TargetCollectionId: uptr(9), TargetTags: EncodeTags(nil), Priority: 50, Active: true,
	}
	if err := svc.CreateRule(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// New rule at the SAME priority routing overlapping hostnames to a DIFFERENT
	// collection → must be flagged as a conflict.
	conflicting := &model.PeerClassificationRule{
		UserId: 1, MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(47), Priority: 50, Active: true,
	}
	if got := svc.FindConflict(conflicting); got == nil {
		t.Error("expected same-priority collection conflict to be detected")
	}

	// Same matcher but DIFFERENT priority → allowed (priority disambiguates).
	okDifferentPriority := &model.PeerClassificationRule{
		UserId: 1, MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(47), Priority: 100, Active: true,
	}
	if got := svc.FindConflict(okDifferentPriority); got != nil {
		t.Errorf("different priority must not conflict, got rule id=%d", got.Id)
	}

	// Same priority but SAME target collection → not a conflict (no ambiguity).
	okSameCollection := &model.PeerClassificationRule{
		UserId: 1, MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(9), Priority: 50, Active: true,
	}
	if got := svc.FindConflict(okSameCollection); got != nil {
		t.Errorf("same target collection must not conflict, got rule id=%d", got.Id)
	}

	// Different user, same priority/overlap → multi-tenant isolation, no conflict.
	otherUser := &model.PeerClassificationRule{
		UserId: 2, MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetCollectionId: uptr(47), Priority: 50, Active: true,
	}
	if got := svc.FindConflict(otherUser); got != nil {
		t.Errorf("cross-user must not conflict, got rule id=%d", got.Id)
	}
}

func TestFindDuplicate(t *testing.T) {
	setupPCTestDB(t)
	svc := &PeerClassificationService{}

	r := &model.PeerClassificationRule{
		UserId: 1, MatcherType: model.MatcherTypeContains, Pattern: "suc47",
		TargetTags: EncodeTags(nil), Priority: 10, Active: true,
	}
	if err := svc.CreateRule(r); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Same (user, type, pattern) case-insensitive → duplicate.
	if svc.FindDuplicate(1, model.MatcherTypeContains, "SUC47", 0) == nil {
		t.Error("expected case-insensitive duplicate to be found")
	}
	// Exclude self → not a duplicate.
	if svc.FindDuplicate(1, model.MatcherTypeContains, "suc47", r.Id) != nil {
		t.Error("excluding self must not report duplicate")
	}
	// Different matcher type → not a duplicate.
	if svc.FindDuplicate(1, model.MatcherTypePrefix, "suc47", 0) != nil {
		t.Error("different matcher type must not be a duplicate")
	}
	// Different user → not a duplicate.
	if svc.FindDuplicate(2, model.MatcherTypeContains, "suc47", 0) != nil {
		t.Error("different user must not be a duplicate")
	}
}
