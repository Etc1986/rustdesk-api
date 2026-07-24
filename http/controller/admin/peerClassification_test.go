package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/global"
	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/service"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

// setupPCHandlerDB wires an in-memory DB with the classification-relevant models
// and the services the handlers touch. (Shares TestMain with the batch test.)
func setupPCHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: glog.Default.LogMode(glog.Silent),
	})
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	if err := db.AutoMigrate(
		&model.PeerClassificationRule{},
		&model.AuditPeerClassification{},
		&model.AuditPeerClassificationItem{},
		&model.AddressBook{},
		&model.AddressBookCollection{},
		&model.Peer{},
		&model.User{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	service.DB = db
	service.AllService = &service.Service{
		AddressBookService:        &service.AddressBookService{},
		PeerService:               &service.PeerService{},
		UserService:               &service.UserService{},
		PeerClassificationService: &service.PeerClassificationService{},
	}
	global.ApiInitValidator() // Pin handler uses global.Validator.ValidStruct
	return db
}

// pcRouter registers the classification routes with an injected current user.
func pcRouter(curUser *model.User) *gin.Engine {
	r := gin.New()
	ct := &PeerClassificationRule{}
	inject := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			if curUser != nil {
				c.Set("curUser", curUser)
			}
			h(c)
		}
	}
	r.POST("/simulate", inject(ct.Simulate))
	r.POST("/apply", inject(ct.Apply))
	r.POST("/pin", inject(ct.Pin))
	return r
}

// makeAdmin returns a *model.User with Id set and IsAdmin=true.
func makeAdmin(id uint) *model.User {
	u := makeUser(id)
	yes := true
	u.IsAdmin = &yes
	return u
}

// pcPostBody posts a JSON body to path and decodes the response.
func pcPostBody(t *testing.T, router *gin.Engine, path string, body interface{}) map[string]interface{} {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	return resp
}

func pcPost(t *testing.T, router *gin.Engine, path string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	return resp
}

func seedRule(db *gorm.DB, userId uint, mtype, pattern string, col uint, priority int) {
	col2 := col
	db.Create(&model.PeerClassificationRule{
		UserId: userId, MatcherType: mtype, Pattern: pattern,
		TargetCollectionId: &col2, TargetTags: service.EncodeTags(nil),
		Priority: priority, Active: true,
	})
}

func seedRuleTags(db *gorm.DB, userId uint, mtype, pattern string, col uint, tags []string, priority int) {
	col2 := col
	db.Create(&model.PeerClassificationRule{
		UserId: userId, MatcherType: mtype, Pattern: pattern,
		TargetCollectionId: &col2, TargetTags: service.EncodeTags(tags),
		Priority: priority, Active: true,
	})
}

func abCount(db *gorm.DB) int64 {
	var n int64
	db.Model(&model.AddressBook{}).Count(&n)
	return n
}

// firstResult returns the first row of the response's results array.
func firstResult(resp map[string]interface{}) map[string]interface{} {
	data, _ := resp["data"].(map[string]interface{})
	results, _ := data["results"].([]interface{})
	if len(results) == 0 {
		return map[string]interface{}{}
	}
	row, _ := results[0].(map[string]interface{})
	return row
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 3: peer matching no rule → simulate and apply leave it intact
// ─────────────────────────────────────────────────────────────────────────────

func TestSimulateApply_NoMatchLeavesPeerIntact(t *testing.T) {
	db := setupPCHandlerDB(t)
	seedRule(db, 1, model.MatcherTypeContains, "suc47", 47, 10)
	db.Create(&model.Peer{Id: "wks-x", Hostname: "wks-plain", UserId: 0, Os: "Windows"})

	router := pcRouter(makeUser(1))

	// Simulate.
	resp := pcPost(t, router, "/simulate")
	if respCode(resp) != 0 {
		t.Fatalf("simulate failed: %v", resp)
	}
	if dataInt(resp, "no_match") != 1 || dataInt(resp, "created") != 0 {
		t.Errorf("want no_match=1 created=0, got %v", resp["data"])
	}
	// Apply.
	resp = pcPost(t, router, "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply failed: %v", resp)
	}
	if abCount(db) != 0 {
		t.Errorf("untouched peer must not be inserted; AB rows=%d", abCount(db))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 5: simulate NEVER writes — row counts identical before/after
// ─────────────────────────────────────────────────────────────────────────────

func TestSimulate_DoesNotWrite(t *testing.T) {
	db := setupPCHandlerDB(t)
	seedRule(db, 1, model.MatcherTypePrefix, "svr-", 5, 10)
	db.Create(&model.Peer{Id: "svr-1", Hostname: "svr-1", UserId: 0, Os: "Windows"})
	db.Create(&model.Peer{Id: "svr-2", Hostname: "svr-2", UserId: 0, Os: "Windows"})

	before := abCount(db)
	var ruleBefore, auditBefore int64
	db.Model(&model.PeerClassificationRule{}).Count(&ruleBefore)
	db.Model(&model.AuditPeerClassification{}).Count(&auditBefore)

	resp := pcPost(t, pcRouter(makeUser(1)), "/simulate")
	if respCode(resp) != 0 {
		t.Fatalf("simulate failed: %v", resp)
	}
	// It should REPORT 2 would-be creations…
	if dataInt(resp, "created") != 2 {
		t.Errorf("simulate should report created=2, got %d", dataInt(resp, "created"))
	}
	// …but write nothing.
	var ruleAfter, auditAfter int64
	db.Model(&model.PeerClassificationRule{}).Count(&ruleAfter)
	db.Model(&model.AuditPeerClassification{}).Count(&auditAfter)
	if abCount(db) != before {
		t.Errorf("simulate wrote address_books: before=%d after=%d", before, abCount(db))
	}
	if auditAfter != auditBefore {
		t.Errorf("simulate wrote an audit row: before=%d after=%d", auditBefore, auditAfter)
	}
	if ruleAfter != ruleBefore {
		t.Errorf("simulate mutated rules: before=%d after=%d", ruleBefore, ruleAfter)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 4: re-evaluation moves an already-classified peer, no duplicate
// ─────────────────────────────────────────────────────────────────────────────

func TestApply_MovesExistingEntryNoDuplicate(t *testing.T) {
	db := setupPCHandlerDB(t)

	// Peer already in AB in collection 3 (placed earlier). Its hostname now
	// reads svr-suc47 (changed), which a rule routes to collection 47.
	db.Create(&model.Peer{Id: "dev-1", Hostname: "svr-suc47", UserId: 0, Os: "Windows", Username: "op"})
	db.Create(&model.AddressBook{
		Id: "dev-1", UserId: 1, CollectionId: 3,
		Hostname: "OLD-NAME", Alias: "manual-alias", Tags: service.EncodeTags([]string{"keep"}),
	})
	seedRule(db, 1, model.MatcherTypeContains, "suc47", 47, 10)

	resp := pcPost(t, pcRouter(makeUser(1)), "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply failed: %v", resp)
	}
	if dataInt(resp, "moved") != 1 || dataInt(resp, "created") != 0 {
		t.Errorf("want moved=1 created=0, got %v", resp["data"])
	}

	// Exactly one AB row for the peer — moved, not duplicated.
	var rows []model.AddressBook
	db.Where("user_id = ? AND id = ?", 1, "dev-1").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 AB row for peer, got %d (duplicate!)", len(rows))
	}
	ab := rows[0]
	if ab.CollectionId != 47 {
		t.Errorf("collection: want 47 (moved), got %d", ab.CollectionId)
	}
	if ab.Alias != "svr-suc47" {
		t.Errorf("alias must mirror live hostname: want svr-suc47, got %q", ab.Alias)
	}
	// Manual tag preserved (union, not destructive).
	tags := service.DecodeTags(ab.Tags)
	found := false
	for _, tg := range tags {
		if tg == "keep" {
			found = true
		}
	}
	if !found {
		t.Errorf("manual tag 'keep' must be preserved by union, got %v", tags)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pinned: a pinned entry is fully protected — collection, alias and tags
// ─────────────────────────────────────────────────────────────────────────────

func TestPinned_SimulateReportsAndApplyProtects(t *testing.T) {
	db := setupPCHandlerDB(t)

	// Peer whose live hostname a rule would route to collection 47 with a tag.
	db.Create(&model.Peer{Id: "dev-1", Hostname: "svr-suc47", UserId: 0, Os: "Windows", Username: "op"})
	// Existing AB entry, PINNED, in collection 3 with a manual alias + tag.
	db.Create(&model.AddressBook{
		Id: "dev-1", UserId: 1, CollectionId: 3, Pinned: true,
		Hostname: "OLD", Alias: "manual-alias", Tags: service.EncodeTags([]string{"keep"}),
	})
	seedRuleTags(db, 1, model.MatcherTypeContains, "suc47", 47, []string{"site"}, 10)

	router := pcRouter(makeUser(1))

	// Simulate: reports the entry as pinned, still shows the would-be proposal,
	// but counts it under pinned_skipped and never as a change.
	resp := pcPost(t, router, "/simulate")
	if respCode(resp) != 0 {
		t.Fatalf("simulate failed: %v", resp)
	}
	if dataInt(resp, "pinned_skipped") != 1 || dataInt(resp, "moved") != 0 {
		t.Errorf("want pinned_skipped=1 moved=0, got %v", resp["data"])
	}
	row := firstResult(resp)
	if row["action"] != "pinned" {
		t.Errorf("action: want pinned, got %v", row["action"])
	}
	if row["pinned"] != true {
		t.Errorf("pinned flag: want true, got %v", row["pinned"])
	}
	if row["changes"] != false {
		t.Errorf("changes: want false for pinned, got %v", row["changes"])
	}
	// Informative: the proposed collection (what the rule WOULD do) is visible.
	if pc, _ := row["proposed_collection_id"].(float64); pc != 47 {
		t.Errorf("proposed_collection_id should still show 47 (informative), got %v", row["proposed_collection_id"])
	}

	// Apply: entry is untouched — collection, alias and tags all preserved.
	resp = pcPost(t, router, "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply failed: %v", resp)
	}
	if dataInt(resp, "pinned_skipped") != 1 || dataInt(resp, "moved") != 0 {
		t.Errorf("apply summary: want pinned_skipped=1 moved=0, got %v", resp["data"])
	}
	var ab model.AddressBook
	db.Where("user_id = ? AND id = ?", 1, "dev-1").First(&ab)
	if ab.CollectionId != 3 {
		t.Errorf("pinned collection changed: want 3, got %d", ab.CollectionId)
	}
	if ab.Alias != "manual-alias" {
		t.Errorf("pinned alias changed: want manual-alias, got %q", ab.Alias)
	}
	tags := service.DecodeTags(ab.Tags)
	if len(tags) != 1 || tags[0] != "keep" {
		t.Errorf("pinned tags changed: want [keep], got %v", tags)
	}
	// Audit records the pinned skip.
	var audits []model.AuditPeerClassification
	db.Find(&audits)
	if len(audits) != 1 || audits[0].PinnedSkipped != 1 {
		t.Errorf("audit pinned_skipped: want 1, got %+v", audits)
	}
}

// TestPinned_NonPinnedUnaffected confirms clearing the pin restores normal
// classification (no regression path).
func TestPinned_NonPinnedUnaffected(t *testing.T) {
	db := setupPCHandlerDB(t)
	db.Create(&model.Peer{Id: "dev-2", Hostname: "svr-suc47", UserId: 0, Os: "Windows"})
	db.Create(&model.AddressBook{
		Id: "dev-2", UserId: 1, CollectionId: 3, Pinned: false,
		Alias: "manual", Tags: service.EncodeTags([]string{"keep"}),
	})
	seedRuleTags(db, 1, model.MatcherTypeContains, "suc47", 47, []string{"site"}, 10)

	resp := pcPost(t, pcRouter(makeUser(1)), "/apply")
	if dataInt(resp, "moved") != 1 || dataInt(resp, "pinned_skipped") != 0 {
		t.Errorf("non-pinned should move: want moved=1 pinned_skipped=0, got %v", resp["data"])
	}
	var ab model.AddressBook
	db.Where("user_id = ? AND id = ?", 1, "dev-2").First(&ab)
	if ab.CollectionId != 47 || ab.Alias != "svr-suc47" {
		t.Errorf("non-pinned not classified: collection=%d alias=%q", ab.CollectionId, ab.Alias)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pin toggle endpoint + user scope
// ─────────────────────────────────────────────────────────────────────────────

func TestPin_ToggleAndScope(t *testing.T) {
	db := setupPCHandlerDB(t)

	// pinnedOf reads the current pinned flag freshly (avoid struct-reuse gotchas).
	pinnedOf := func(rowId uint) bool {
		var r model.AddressBook
		db.Where("row_id = ?", rowId).First(&r)
		return r.Pinned
	}

	// AB entry owned by user 1, and one owned by user 2.
	ab1 := &model.AddressBook{Id: "p1", UserId: 1, CollectionId: 0, Tags: service.EncodeTags(nil)}
	db.Create(ab1)
	ab2 := &model.AddressBook{Id: "p2", UserId: 2, CollectionId: 0, Tags: service.EncodeTags(nil)}
	db.Create(ab2)

	// User 1 pins their own entry.
	resp := pcPostBody(t, pcRouter(makeUser(1)), "/pin", map[string]interface{}{"row_id": ab1.RowId, "pinned": true})
	if respCode(resp) != 0 {
		t.Fatalf("self-pin failed: %v", resp)
	}
	if !pinnedOf(ab1.RowId) {
		t.Error("entry should be pinned after toggle on")
	}

	// User 1 unpins their own entry.
	resp = pcPostBody(t, pcRouter(makeUser(1)), "/pin", map[string]interface{}{"row_id": ab1.RowId, "pinned": false})
	if respCode(resp) != 0 {
		t.Fatalf("self-unpin failed: %v", resp)
	}
	if pinnedOf(ab1.RowId) {
		t.Error("entry should be unpinned after toggle off")
	}

	// User 1 (non-admin) tries to pin user 2's entry → rejected, unchanged.
	resp = pcPostBody(t, pcRouter(makeUser(1)), "/pin", map[string]interface{}{"row_id": ab2.RowId, "pinned": true})
	if respCode(resp) == 0 {
		t.Error("cross-user pin by non-admin must be rejected")
	}
	if pinnedOf(ab2.RowId) {
		t.Error("user 2's entry must remain unpinned after rejected cross-user attempt")
	}

	// An admin CAN pin another user's entry.
	resp = pcPostBody(t, pcRouter(makeAdmin(9)), "/pin", map[string]interface{}{"row_id": ab2.RowId, "pinned": true})
	if respCode(resp) != 0 {
		t.Fatalf("admin cross-user pin should succeed: %v", resp)
	}
	if !pinnedOf(ab2.RowId) {
		t.Error("admin pin should have taken effect")
	}

	// Missing entry → ItemNotFound.
	resp = pcPostBody(t, pcRouter(makeUser(1)), "/pin", map[string]interface{}{"row_id": 99999, "pinned": true})
	if respCode(resp) == 0 {
		t.Error("pinning a non-existent entry must fail")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 7: multi-tenant isolation — user A's rules don't touch user B's peers
// ─────────────────────────────────────────────────────────────────────────────

// TestApply_MultiTenantIsolation verifies the CORRECT multi-tenant model: the
// engine evaluates ALL peers (peers are global, user_id is not device ownership
// in Lejianwen), but isolation comes from (a) rules being per-user and (b) the
// address_book entries being owned by the running user.
func TestApply_MultiTenantIsolation(t *testing.T) {
	db := setupPCHandlerDB(t)

	// Two global peers (user_id = 0, the real Lejianwen case). Only user 1 has a
	// rule; user 2 has none.
	seedRule(db, 1, model.MatcherTypePrefix, "svr-", 5, 10)
	db.Create(&model.Peer{Id: "svr-a", Hostname: "svr-a", UserId: 0, Os: "Windows"})
	db.Create(&model.Peer{Id: "svr-b", Hostname: "svr-b", UserId: 0, Os: "Windows"})

	// Apply as user 2 (no rules): every peer is evaluated, but nothing matches →
	// nothing is created. Rules are what's scoped, not the peer set.
	resp := pcPost(t, pcRouter(makeUser(2)), "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply(user2) failed: %v", resp)
	}
	if dataInt(resp, "total") != 2 || dataInt(resp, "no_match") != 2 || dataInt(resp, "created") != 0 {
		t.Errorf("user2 (no rules) should evaluate 2 peers, match none: got %v", resp["data"])
	}
	if abCount(db) != 0 {
		t.Errorf("user2 apply must not create anything; AB rows=%d", abCount(db))
	}

	// Apply as user 1: all peers evaluated against user 1's rule → both created,
	// owned by user 1 (not by the peers' user_id=0).
	resp = pcPost(t, pcRouter(makeUser(1)), "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply(user1) failed: %v", resp)
	}
	if dataInt(resp, "total") != 2 || dataInt(resp, "created") != 2 {
		t.Errorf("user1 should create both peers: got %v", resp["data"])
	}
	var rows []model.AddressBook
	db.Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("want 2 AB rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.UserId != 1 || r.CollectionId != 5 {
			t.Errorf("entry must be owned by running user 1 in collection 5, got user=%d col=%d", r.UserId, r.CollectionId)
		}
	}
}

// TestSimulateApply_EvaluatesPeersWithUserIdZero is the regression test for the
// lab bug: peers with user_id = 0 (every peer in Lejianwen, including real
// devices) must be evaluated by simulate and apply. Before the fix the engine
// filtered peers by user_id and returned Total 0.
func TestSimulateApply_EvaluatesPeersWithUserIdZero(t *testing.T) {
	db := setupPCHandlerDB(t)

	// The realistic lab shape: admin is user 1, peers all have user_id = 0.
	seedRule(db, 1, model.MatcherTypeContains, "suc47", 47, 10)
	db.Create(&model.Peer{Id: "real-1", Hostname: "svr-suc47", UserId: 0, Os: "Windows", Username: "op"})
	db.Create(&model.Peer{Id: "real-2", Hostname: "wks-plain", UserId: 0, Os: "Windows"})

	router := pcRouter(makeUser(1))

	// Simulate must SEE the peers (Total 2, not 0).
	resp := pcPost(t, router, "/simulate")
	if respCode(resp) != 0 {
		t.Fatalf("simulate failed: %v", resp)
	}
	if dataInt(resp, "total") != 2 {
		t.Fatalf("regression: simulate must evaluate user_id=0 peers, got total=%d", dataInt(resp, "total"))
	}
	if dataInt(resp, "created") != 1 || dataInt(resp, "no_match") != 1 {
		t.Errorf("want created=1 no_match=1, got %v", resp["data"])
	}

	// Apply must classify the matching user_id=0 peer into user 1's address book.
	resp = pcPost(t, router, "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply failed: %v", resp)
	}
	if dataInt(resp, "total") != 2 || dataInt(resp, "created") != 1 {
		t.Errorf("apply want total=2 created=1, got %v", resp["data"])
	}
	var ab model.AddressBook
	db.Where("id = ?", "real-1").First(&ab)
	if ab.UserId != 1 || ab.CollectionId != 47 || ab.Alias != "svr-suc47" {
		t.Errorf("user_id=0 peer not classified into user 1's book: %+v", ab)
	}
}
