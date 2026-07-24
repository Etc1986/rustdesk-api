package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
	return db
}

// pcRouter registers the simulate/apply routes with an injected current user.
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
	return r
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

func abCount(db *gorm.DB) int64 {
	var n int64
	db.Model(&model.AddressBook{}).Count(&n)
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Test point 3: peer matching no rule → simulate and apply leave it intact
// ─────────────────────────────────────────────────────────────────────────────

func TestSimulateApply_NoMatchLeavesPeerIntact(t *testing.T) {
	db := setupPCHandlerDB(t)
	seedRule(db, 1, model.MatcherTypeContains, "suc47", 47, 10)
	db.Create(&model.Peer{Id: "wks-x", Hostname: "wks-plain", UserId: 1, Os: "Windows"})

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
	db.Create(&model.Peer{Id: "svr-1", Hostname: "svr-1", UserId: 1, Os: "Windows"})
	db.Create(&model.Peer{Id: "svr-2", Hostname: "svr-2", UserId: 1, Os: "Windows"})

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
	db.Create(&model.Peer{Id: "dev-1", Hostname: "svr-suc47", UserId: 1, Os: "Windows", Username: "op"})
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
// Test point 7: multi-tenant isolation — user A's rules don't touch user B's peers
// ─────────────────────────────────────────────────────────────────────────────

func TestApply_MultiTenantIsolation(t *testing.T) {
	db := setupPCHandlerDB(t)

	// Only user 1 owns a rule. Both users own a matching-looking peer.
	seedRule(db, 1, model.MatcherTypePrefix, "svr-", 5, 10)
	db.Create(&model.Peer{Id: "svr-u1", Hostname: "svr-u1", UserId: 1, Os: "Windows"})
	db.Create(&model.Peer{Id: "svr-u2", Hostname: "svr-u2", UserId: 2, Os: "Windows"})

	// Apply as user 2: no rules for user 2 → their peer untouched.
	resp := pcPost(t, pcRouter(makeUser(2)), "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply(user2) failed: %v", resp)
	}
	if dataInt(resp, "total") != 1 || dataInt(resp, "no_match") != 1 || dataInt(resp, "created") != 0 {
		t.Errorf("user2 should see only their 1 peer, no match: got %v", resp["data"])
	}
	if abCount(db) != 0 {
		t.Errorf("user2 apply must not create anything; AB rows=%d", abCount(db))
	}

	// Apply as user 1: only their peer is evaluated and created.
	resp = pcPost(t, pcRouter(makeUser(1)), "/apply")
	if respCode(resp) != 0 {
		t.Fatalf("apply(user1) failed: %v", resp)
	}
	if dataInt(resp, "total") != 1 || dataInt(resp, "created") != 1 {
		t.Errorf("user1 should create their 1 peer: got %v", resp["data"])
	}
	// The only AB row belongs to user 1's peer.
	var rows []model.AddressBook
	db.Find(&rows)
	if len(rows) != 1 || rows[0].Id != "svr-u1" || rows[0].UserId != 1 {
		t.Errorf("cross-tenant leak: AB rows=%+v", rows)
	}
}
