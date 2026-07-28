package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/service"
	"gorm.io/gorm"
)

// The secret used across these tests. Assertions check it never leaks into the
// audit trail, so it must be distinctive enough to grep for.
const testSecret = "sup3rs3cr3t-peer-pw"

// passwordRouter builds a gin router exposing BatchSetPassword and injecting a
// fake admin user, mirroring testRouter in addressBook_batch_test.go.
func passwordRouter(adminUser *model.User) *gin.Engine {
	r := gin.New()
	ct := &AddressBook{}
	r.POST("/batchSetPassword", func(c *gin.Context) {
		if adminUser != nil {
			c.Set("curUser", adminUser)
		}
		ct.BatchSetPassword(c)
	})
	return r
}

// postPassword serialises body to JSON, posts it to the batchSetPassword route
// and returns the decoded response map.
func postPassword(t *testing.T, router *gin.Engine, body interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/batchSetPassword", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	return resp
}

// seedEntry inserts one address-book entry and returns it with its RowId set.
func seedEntry(t *testing.T, db *gorm.DB, id string, userId, collectionId uint, password string) *model.AddressBook {
	t.Helper()
	ab := &model.AddressBook{
		Id:           id,
		UserId:       userId,
		CollectionId: collectionId,
		Password:     password,
		Tags:         []byte(`[]`),
	}
	if err := db.Create(ab).Error; err != nil {
		t.Fatalf("seed entry %s: %v", id, err)
	}
	return ab
}

// passwordOf reloads one entry and returns its stored password.
func passwordOf(t *testing.T, db *gorm.DB, rowId uint) string {
	t.Helper()
	var ab model.AddressBook
	if err := db.Where("row_id = ?", rowId).First(&ab).Error; err != nil {
		t.Fatalf("reload entry %d: %v", rowId, err)
	}
	return ab.Password
}

// statusOf digs the per-entry status out of the granular results array.
func statusOf(t *testing.T, resp map[string]interface{}, rowId uint) string {
	t.Helper()
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response carries no data object: %v", resp)
	}
	results, _ := data["results"].([]interface{})
	for _, r := range results {
		entry, _ := r.(map[string]interface{})
		if entry == nil {
			continue
		}
		if uint(entry["row_id"].(float64)) == rowId {
			s, _ := entry["status"].(string)
			return s
		}
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Ownership
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchSetPassword_RejectsWrongCollectionOwner verifies that targeting a
// collection owned by someone other than the stated user_id is refused before
// any write happens.
func TestBatchSetPassword_RejectsWrongCollectionOwner(t *testing.T) {
	db := setupHandlerDB(t)

	col := &model.AddressBookCollection{UserId: 7, Name: "SecretPool"}
	db.Create(col)
	ab := seedEntry(t, db, "pc-a", 7, col.Id, "original")

	router := passwordRouter(makeUser(99))
	resp := postPassword(t, router, map[string]interface{}{
		"user_id":       99, // guard says user 99...
		"collection_id": col.Id,
		"password":      testSecret,
	})

	if respCode(resp) != 101 {
		t.Errorf("expected rejection code 101, got %d — %v", respCode(resp), resp)
	}
	if got := passwordOf(t, db, ab.RowId); got != "original" {
		t.Errorf("ownership bypass: password was rewritten to %q", got)
	}
}

// TestBatchSetPassword_RejectsMissingCollection verifies a non-existent
// collection is refused, and is refused the same opaque way as one owned by
// somebody else — the caller must not be able to probe which collections exist.
func TestBatchSetPassword_RejectsMissingCollection(t *testing.T) {
	setupHandlerDB(t)

	router := passwordRouter(makeUser(1))
	resp := postPassword(t, router, map[string]interface{}{
		"collection_id": 4242,
		"password":      testSecret,
	})

	if respCode(resp) != 101 {
		t.Errorf("expected rejection code 101, got %d — %v", respCode(resp), resp)
	}
}

// TestBatchSetPassword_RowIdsGuardBlocksOtherUsers verifies that when the
// optional user_id guard is supplied, entries belonging to another user are
// reported as forbidden and left untouched, while the in-scope ones still go
// through.
func TestBatchSetPassword_RowIdsGuardBlocksOtherUsers(t *testing.T) {
	db := setupHandlerDB(t)

	mine := seedEntry(t, db, "pc-mine", 5, 0, "old-mine")
	theirs := seedEntry(t, db, "pc-theirs", 6, 0, "old-theirs")

	router := passwordRouter(makeUser(1))
	resp := postPassword(t, router, map[string]interface{}{
		"user_id":  5,
		"row_ids":  []uint{mine.RowId, theirs.RowId},
		"password": testSecret,
	})

	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}
	if got := statusOf(t, resp, theirs.RowId); got != "forbidden" {
		t.Errorf("out-of-scope entry: want status forbidden, got %q", got)
	}
	if got := passwordOf(t, db, theirs.RowId); got != "old-theirs" {
		t.Errorf("guard bypass: other user's password became %q", got)
	}
	if got := passwordOf(t, db, mine.RowId); got != testSecret {
		t.Errorf("in-scope entry should have been updated, got %q", got)
	}
	if dataInt(resp, "updated") != 1 || dataInt(resp, "skipped") != 1 {
		t.Errorf("want updated=1 skipped=1, got %v", resp["data"])
	}
}

// TestBatchSetPassword_CrossUserAllowedWithoutGuard documents the deliberate
// counterpart: with no user_id guard an admin may set one password across
// several users' books, because the same machine legitimately appears in more
// than one of them.
func TestBatchSetPassword_CrossUserAllowedWithoutGuard(t *testing.T) {
	db := setupHandlerDB(t)

	a := seedEntry(t, db, "shared-pc", 5, 0, "old")
	b := seedEntry(t, db, "shared-pc", 6, 0, "old")

	router := passwordRouter(makeUser(1))
	resp := postPassword(t, router, map[string]interface{}{
		"row_ids":  []uint{a.RowId, b.RowId},
		"password": testSecret,
	})

	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}
	if dataInt(resp, "updated") != 2 {
		t.Errorf("want updated=2, got %v", resp["data"])
	}
	for _, e := range []*model.AddressBook{a, b} {
		if got := passwordOf(t, db, e.RowId); got != testSecret {
			t.Errorf("entry %d not updated: %q", e.RowId, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request shape
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchSetPassword_RejectsAmbiguousOrEmptyTarget verifies the XOR rule on
// the target selector and the non-empty password requirement.
func TestBatchSetPassword_RejectsAmbiguousOrEmptyTarget(t *testing.T) {
	db := setupHandlerDB(t)
	col := &model.AddressBookCollection{UserId: 1, Name: "Pool"}
	db.Create(col)
	ab := seedEntry(t, db, "pc-x", 1, col.Id, "original")

	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"both selectors", map[string]interface{}{
			"row_ids": []uint{ab.RowId}, "collection_id": col.Id, "password": testSecret}},
		{"neither selector", map[string]interface{}{
			"password": testSecret}},
		{"empty row_ids", map[string]interface{}{
			"row_ids": []uint{}, "password": testSecret}},
		{"missing password", map[string]interface{}{
			"row_ids": []uint{ab.RowId}}},
		{"empty password", map[string]interface{}{
			"row_ids": []uint{ab.RowId}, "password": ""}},
	}

	router := passwordRouter(makeUser(1))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postPassword(t, router, tc.body)
			if respCode(resp) != 101 {
				t.Errorf("expected rejection code 101, got %d — %v", respCode(resp), resp)
			}
			if got := passwordOf(t, db, ab.RowId); got != "original" {
				t.Errorf("rejected request still wrote: password is %q", got)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Granular response
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchSetPassword_GranularResults exercises the mixed case: one entry to
// update, one already carrying the same password, one row id that does not
// exist. Each must be classified individually and the counters must add up.
func TestBatchSetPassword_GranularResults(t *testing.T) {
	db := setupHandlerDB(t)

	toUpdate := seedEntry(t, db, "pc-1", 3, 0, "old")
	alreadySet := seedEntry(t, db, "pc-2", 3, 0, testSecret)
	const ghostRowId = 999999

	router := passwordRouter(makeUser(10))
	resp := postPassword(t, router, map[string]interface{}{
		"row_ids":  []uint{toUpdate.RowId, alreadySet.RowId, ghostRowId},
		"password": testSecret,
	})

	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}
	if dataInt(resp, "total") != 3 {
		t.Errorf("want total=3, got %v", resp["data"])
	}
	if dataInt(resp, "updated") != 1 {
		t.Errorf("want updated=1, got %v", resp["data"])
	}
	if dataInt(resp, "skipped") != 1 {
		t.Errorf("want skipped=1 (the unchanged one), got %v", resp["data"])
	}
	if dataInt(resp, "not_found") != 1 {
		t.Errorf("want not_found=1, got %v", resp["data"])
	}
	if dataInt(resp, "failed") != 0 {
		t.Errorf("want failed=0, got %v", resp["data"])
	}

	if got := statusOf(t, resp, toUpdate.RowId); got != "updated" {
		t.Errorf("entry to update: want status updated, got %q", got)
	}
	if got := statusOf(t, resp, alreadySet.RowId); got != "unchanged" {
		t.Errorf("already-correct entry: want status unchanged, got %q", got)
	}
	if got := statusOf(t, resp, ghostRowId); got != "not_found" {
		t.Errorf("missing row: want status not_found, got %q", got)
	}
}

// TestBatchSetPassword_ByCollection verifies collection mode targets every
// entry filed under it, and only those.
func TestBatchSetPassword_ByCollection(t *testing.T) {
	db := setupHandlerDB(t)

	col := &model.AddressBookCollection{UserId: 4, Name: "Fleet"}
	db.Create(col)
	other := &model.AddressBookCollection{UserId: 4, Name: "Other"}
	db.Create(other)

	in1 := seedEntry(t, db, "pc-a", 4, col.Id, "old")
	in2 := seedEntry(t, db, "pc-b", 4, col.Id, "old")
	outside := seedEntry(t, db, "pc-c", 4, other.Id, "old")

	router := passwordRouter(makeUser(10))
	resp := postPassword(t, router, map[string]interface{}{
		"collection_id": col.Id,
		"password":      testSecret,
	})

	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}
	if dataInt(resp, "total") != 2 || dataInt(resp, "updated") != 2 {
		t.Errorf("want total=2 updated=2, got %v", resp["data"])
	}
	for _, e := range []*model.AddressBook{in1, in2} {
		if got := passwordOf(t, db, e.RowId); got != testSecret {
			t.Errorf("entry %d in collection not updated: %q", e.RowId, got)
		}
	}
	if got := passwordOf(t, db, outside.RowId); got != "old" {
		t.Errorf("entry outside the collection was touched: %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Transaction
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchSetPassword_RollsBackOnFailure drives the service directly with a
// row id that disappears between resolution and write. The whole set must roll
// back — a partial credential assignment is worse than none, because the
// operator would believe every listed machine now shares one password.
func TestBatchSetPassword_RollsBackOnFailure(t *testing.T) {
	db := setupHandlerDB(t)

	first := seedEntry(t, db, "pc-1", 1, 0, "old")
	second := seedEntry(t, db, "pc-2", 1, 0, "old")

	err := service.AllService.AddressBookService.
		BatchSetPassword([]uint{first.RowId, second.RowId, 999999}, testSecret)
	if err == nil {
		t.Fatal("expected an error when one target row does not exist")
	}

	for _, e := range []*model.AddressBook{first, second} {
		if got := passwordOf(t, db, e.RowId); got != "old" {
			t.Errorf("entry %d was committed despite rollback: %q", e.RowId, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit
// ─────────────────────────────────────────────────────────────────────────────

// TestBatchSetPassword_AuditRecordsOperation verifies one audit row per
// invocation carrying who / which user / which collection / how many.
func TestBatchSetPassword_AuditRecordsOperation(t *testing.T) {
	db := setupHandlerDB(t)

	col := &model.AddressBookCollection{UserId: 4, Name: "Fleet"}
	db.Create(col)
	seedEntry(t, db, "pc-a", 4, col.Id, "old")
	seedEntry(t, db, "pc-b", 4, col.Id, testSecret) // already set → skipped

	router := passwordRouter(makeUser(10))
	resp := postPassword(t, router, map[string]interface{}{
		"collection_id": col.Id,
		"password":      testSecret,
	})
	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}

	var audits []model.AuditAbPassword
	db.Find(&audits)
	if len(audits) != 1 {
		t.Fatalf("expected exactly 1 audit row, got %d", len(audits))
	}
	a := audits[0]
	if a.AdminId != 10 {
		t.Errorf("audit admin_id: want 10, got %d", a.AdminId)
	}
	if a.CollectionId != col.Id {
		t.Errorf("audit collection_id: want %d, got %d", col.Id, a.CollectionId)
	}
	if a.UserId != 4 {
		t.Errorf("audit user_id should fall back to the collection owner: got %d", a.UserId)
	}
	if a.Total != 2 || a.Updated != 1 || a.Skipped != 1 {
		t.Errorf("audit counters: want total=2 updated=1 skipped=1, got %+v", a)
	}
	if time.Time(a.CreatedAt).IsZero() {
		t.Error("audit row must carry a creation timestamp")
	}
}

// TestBatchSetPassword_AuditNeverStoresPassword reads the audit table back as
// raw column/value pairs and asserts the secret appears nowhere in it.
//
// Deliberately raw rather than field-by-field: a future change that adds a
// password column to model.AuditAbPassword would slip past any assertion
// written against the struct's current fields, and would quietly start
// persisting plaintext credentials into the audit trail. This fails instead.
func TestBatchSetPassword_AuditNeverStoresPassword(t *testing.T) {
	db := setupHandlerDB(t)

	ab := seedEntry(t, db, "pc-a", 4, 0, "old")

	router := passwordRouter(makeUser(10))
	resp := postPassword(t, router, map[string]interface{}{
		"row_ids":  []uint{ab.RowId},
		"password": testSecret,
	})
	if respCode(resp) != 0 {
		t.Fatalf("request failed: %v", resp)
	}
	// Sanity: the password did land where it belongs.
	if got := passwordOf(t, db, ab.RowId); got != testSecret {
		t.Fatalf("entry was not updated, test would pass vacuously: %q", got)
	}

	var rows []map[string]interface{}
	if err := db.Table("audit_ab_passwords").Find(&rows).Error; err != nil {
		t.Fatalf("read audit table: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	for col, val := range rows[0] {
		if fmt.Sprintf("%v", val) == testSecret {
			t.Errorf("audit column %q leaked the password", col)
		}
	}
}
