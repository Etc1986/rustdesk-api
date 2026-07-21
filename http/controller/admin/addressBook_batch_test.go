package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/global"
	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/service"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/language"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

// TestMain sets up the process-wide globals that the handler depends on.
// Tests in this package run sequentially; each test gets a fresh in-memory DB
// via setupHandlerDB.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)

	// Silence logrus output (TranslateMsg warns on missing i18n keys).
	l := log.New()
	l.SetOutput(io.Discard)
	global.Logger = l

	// Minimal i18n: empty bundle — TranslateMsg falls back to the message ID
	// string when no translation is found, which is fine for assertions.
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	global.Localizer = func(lang string) *i18n.Localizer {
		return i18n.NewLocalizer(bundle, "en")
	}

	os.Exit(m.Run())
}

// setupHandlerDB opens a fresh in-memory SQLite DB, AutoMigrates the
// relevant models, and wires service.DB / service.AllService for the test.
func setupHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: glog.Default.LogMode(glog.Silent),
	})
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AddressBook{},
		&model.AddressBookCollection{},
		&model.Peer{},
		&model.AuditAbBatch{},
		&model.User{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	service.DB = db
	service.AllService = &service.Service{
		AddressBookService: &service.AddressBookService{},
		PeerService:        &service.PeerService{},
		UserService:        &service.UserService{},
	}
	return db
}

// makeUser returns a *model.User with only the Id set; avoids struct-literal
// issues with the embedded IdModel type.
func makeUser(id uint) *model.User {
	u := &model.User{}
	u.Id = id
	return u
}

// testRouter builds a gin router that registers BatchCreateFromPeers at
// POST /batchCreateFromPeers and injects a fake admin user into the context.
func testRouter(adminUser *model.User) *gin.Engine {
	r := gin.New()
	ct := &AddressBook{}
	r.POST("/batchCreateFromPeers", func(c *gin.Context) {
		if adminUser != nil {
			c.Set("curUser", adminUser)
		}
		ct.BatchCreateFromPeers(c)
	})
	return r
}

// doPost is a test helper that serialises body to JSON, posts it, and
// returns the decoded response map.
func doPost(t *testing.T, router *gin.Engine, body interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/batchCreateFromPeers", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	return resp
}

// respCode reads the integer code field from the response map.
func respCode(resp map[string]interface{}) int {
	v, _ := resp["code"].(float64)
	return int(v)
}

// dataInt reads a float64 counter from the nested data object.
func dataInt(resp map[string]interface{}, key string) int {
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		return -1
	}
	v, _ := data[key].(float64)
	return int(v)
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 1: ownership security check
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchCreateFromPeers_RejectsWrongCollectionOwner verifies that a request
// whose collection_id belongs to a different user is rejected with code 101
// before any DB write occurs.
func TestBatchCreateFromPeers_RejectsWrongCollectionOwner(t *testing.T) {
	db := setupHandlerDB(t)

	// Collection owned by user 7.
	col := &model.AddressBookCollection{UserId: 7, Name: "SecretPool"}
	db.Create(col)

	// Seed a peer so the peer_ids list is non-empty.
	peer := &model.Peer{Id: "rdp001", Hostname: "PC-A"}
	db.Create(peer)
	var loaded model.Peer
	db.Where("id = ?", "rdp001").First(&loaded)

	router := testRouter(makeUser(99)) // attacker is user 99
	resp := doPost(t, router, map[string]interface{}{
		"user_id":       99,          // attacker
		"collection_id": col.Id,     // belongs to user 7 — not the attacker
		"peer_ids":      []uint{loaded.RowId},
	})

	if respCode(resp) != 101 {
		t.Errorf("expected rejection code 101, got %d — full response: %v", respCode(resp), resp)
	}

	// Verify: no AB rows inserted.
	var count int64
	db.Model(&model.AddressBook{}).Count(&count)
	if count != 0 {
		t.Errorf("ownership bypass: %d address-book rows were inserted", count)
	}
}

// TestBatchCreateFromPeers_AllowsCorrectOwner verifies that the correct owner
// is not rejected.
func TestBatchCreateFromPeers_AllowsCorrectOwner(t *testing.T) {
	db := setupHandlerDB(t)

	col := &model.AddressBookCollection{UserId: 7, Name: "MyPool"}
	db.Create(col)

	peer := &model.Peer{Id: "rdp002", Hostname: "PC-B", Os: "Windows 11"}
	db.Create(peer)
	var loaded model.Peer
	db.Where("id = ?", "rdp002").First(&loaded)

	router := testRouter(makeUser(7))
	resp := doPost(t, router, map[string]interface{}{
		"user_id":       7,
		"collection_id": col.Id,
		"peer_ids":      []uint{loaded.RowId},
	})

	if respCode(resp) != 0 {
		t.Errorf("correct owner was rejected: %v", resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 3: granular response — classification correctness
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchCreateFromPeers_GranularResponse sends a mixed batch:
//   - peer1: valid, new → "added"
//   - peer2: valid, but already in address book → "existing"
//   - 99999: non-existent row_id → "not_found"
//
// Verifies that the response counters and per-peer statuses are correct.
func TestBatchCreateFromPeers_GranularResponse(t *testing.T) {
	db := setupHandlerDB(t)

	// Seed two peers.
	db.Create(&model.Peer{Id: "p-new", Hostname: "NEW-PC", Os: "Linux Ubuntu 22.04"})
	db.Create(&model.Peer{Id: "p-dup", Hostname: "DUP-PC", Os: "Windows 10"})

	var pNew, pDup model.Peer
	db.Where("id = ?", "p-new").First(&pNew)
	db.Where("id = ?", "p-dup").First(&pDup)

	// Pre-seed address book entry for pDup (user=5, collection=0).
	tags, _ := json.Marshal([]string{})
	db.Create(&model.AddressBook{Id: "p-dup", UserId: 5, CollectionId: 0, Tags: tags})

	router := testRouter(makeUser(1))
	resp := doPost(t, router, map[string]interface{}{
		"user_id":       5,
		"collection_id": 0,
		"peer_ids":      []uint{pNew.RowId, pDup.RowId, 99999},
	})

	if respCode(resp) != 0 {
		t.Fatalf("expected success, got code %d: %v", respCode(resp), resp)
	}

	if dataInt(resp, "total") != 3 {
		t.Errorf("total: want 3, got %d", dataInt(resp, "total"))
	}
	if dataInt(resp, "added") != 1 {
		t.Errorf("added: want 1, got %d", dataInt(resp, "added"))
	}
	if dataInt(resp, "existing") != 1 {
		t.Errorf("existing: want 1, got %d", dataInt(resp, "existing"))
	}
	if dataInt(resp, "not_found") != 1 {
		t.Errorf("not_found: want 1, got %d", dataInt(resp, "not_found"))
	}
	if dataInt(resp, "failed") != 0 {
		t.Errorf("failed: want 0, got %d", dataInt(resp, "failed"))
	}

	// Verify per-peer statuses in results array.
	data := resp["data"].(map[string]interface{})
	results := data["results"].([]interface{})
	statusByPeerId := make(map[float64]string, len(results))
	for _, r := range results {
		row := r.(map[string]interface{})
		pid := row["peer_id"].(float64)
		statusByPeerId[pid] = row["status"].(string)
	}
	if statusByPeerId[float64(pNew.RowId)] != "added" {
		t.Errorf("peer %d (new): want status=added, got %q", pNew.RowId, statusByPeerId[float64(pNew.RowId)])
	}
	if statusByPeerId[float64(pDup.RowId)] != "existing" {
		t.Errorf("peer %d (dup): want status=existing, got %q", pDup.RowId, statusByPeerId[float64(pDup.RowId)])
	}
	if statusByPeerId[99999] != "not_found" {
		t.Errorf("peer 99999 (missing): want status=not_found, got %q", statusByPeerId[99999])
	}

	// Only pNew should have been inserted into address_books.
	var count int64
	db.Model(&model.AddressBook{}).Where("id = ?", "p-new").Count(&count)
	if count != 1 {
		t.Errorf("expected p-new to be inserted, got %d rows", count)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 4: audit row created for every run
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchCreateFromPeers_AuditCreated verifies that each successful call
// to BatchCreateFromPeers produces exactly one audit_ab_batches row with
// correct counters, and that a second call adds a second row.
func TestBatchCreateFromPeers_AuditCreated(t *testing.T) {
	db := setupHandlerDB(t)

	db.Create(&model.Peer{Id: "a1", Hostname: "AUDIT-1", Os: "Windows 11"})
	var p model.Peer
	db.Where("id = ?", "a1").First(&p)

	router := testRouter(makeUser(10)) // admin user 10

	body := map[string]interface{}{
		"user_id":       3,
		"collection_id": 0,
		"peer_ids":      []uint{p.RowId},
	}

	// First call.
	resp1 := doPost(t, router, body)
	if respCode(resp1) != 0 {
		t.Fatalf("first call failed: %v", resp1)
	}

	// Second call (same peer → now "existing").
	resp2 := doPost(t, router, body)
	if respCode(resp2) != 0 {
		t.Fatalf("second call failed: %v", resp2)
	}

	var audits []model.AuditAbBatch
	db.Find(&audits)
	if len(audits) != 2 {
		t.Fatalf("expected 2 audit rows, got %d", len(audits))
	}

	// First run: added=1, existing=0.
	if audits[0].Added != 1 || audits[0].Existing != 0 {
		t.Errorf("audit[0]: want added=1 existing=0, got added=%d existing=%d",
			audits[0].Added, audits[0].Existing)
	}
	if audits[0].AdminId != 10 || audits[0].UserId != 3 {
		t.Errorf("audit[0] ids: want admin=10 user=3, got admin=%d user=%d",
			audits[0].AdminId, audits[0].UserId)
	}

	// Second run: added=0, existing=1.
	if audits[1].Added != 0 || audits[1].Existing != 1 {
		t.Errorf("audit[1]: want added=0 existing=1, got added=%d existing=%d",
			audits[1].Added, audits[1].Existing)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scale: 2 / 10 / 100 peers through the full HTTP handler
// ─────────────────────────────────────────────────────────────────────────────

func runHandlerScale(t *testing.T, n int) {
	t.Helper()
	db := setupHandlerDB(t)

	// Seed n peers.
	for i := 0; i < n; i++ {
		db.Create(&model.Peer{
			Id:       fmt.Sprintf("s-%04d", i),
			Hostname: fmt.Sprintf("HOST-%04d", i),
			Os:       "Linux Ubuntu 22.04",
			Alias:    fmt.Sprintf("Scale %d", i),
		})
	}
	var loaded []model.Peer
	db.Find(&loaded)

	ids := make([]uint, len(loaded))
	for i, p := range loaded {
		ids[i] = p.RowId
	}

	router := testRouter(makeUser(1))
	resp := doPost(t, router, map[string]interface{}{
		"user_id":       2,
		"collection_id": 0,
		"peer_ids":      ids,
	})

	if respCode(resp) != 0 {
		t.Fatalf("scale n=%d: unexpected error code %d: %v", n, respCode(resp), resp)
	}
	if dataInt(resp, "added") != n {
		t.Errorf("scale n=%d: want added=%d, got %d", n, n, dataInt(resp, "added"))
	}
	if dataInt(resp, "not_found") != 0 {
		t.Errorf("scale n=%d: unexpected not_found=%d", n, dataInt(resp, "not_found"))
	}

	var count int64
	db.Model(&model.AddressBook{}).Count(&count)
	if count != int64(n) {
		t.Errorf("scale n=%d: want %d AB rows, got %d", n, n, count)
	}
}

func TestHandlerScale_2Peers(t *testing.T)   { runHandlerScale(t, 2) }
func TestHandlerScale_10Peers(t *testing.T)  { runHandlerScale(t, 10) }
func TestHandlerScale_100Peers(t *testing.T) { runHandlerScale(t, 100) }
