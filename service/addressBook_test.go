package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lejianwen/rustdesk-api/v2/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupABTestDB opens an in-memory SQLite database, runs AutoMigrate for the
// models relevant to address-book batch operations, and wires it to the
// package-level DB variable used by every service method.
func setupABTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
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
	DB = db
	AllService = &Service{
		AddressBookService: &AddressBookService{},
		PeerService:        &PeerService{},
		UserService:        &UserService{},
	}
	return db
}

// tagBytes returns a JSON-encoded empty tag slice, satisfying the not-null
// constraint on address_books.tags.
func tagBytes() []byte {
	b, _ := json.Marshal([]string{})
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 5 (minor): alias copy
// ─────────────────────────────────────────────────────────────────────────────

// TestFromPeer_CopiesAlias verifies that FromPeer propagates peer.Alias to
// the address-book entry (bug fix: previously alias was always empty).
func TestFromPeer_CopiesAlias(t *testing.T) {
	svc := &AddressBookService{}
	peer := &model.Peer{
		Id:       "abc123",
		Hostname: "LAPTOP-01",
		Username: "alice",
		Os:       "Windows 11",
		Alias:    "Alice Laptop",
	}
	ab := svc.FromPeer(peer)
	if ab.Alias != peer.Alias {
		t.Errorf("FromPeer alias: want %q, got %q", peer.Alias, ab.Alias)
	}
}

// TestFromPeer_EmptyAliasWhenPeerHasNone verifies no regression when peer has no alias.
func TestFromPeer_EmptyAliasWhenPeerHasNone(t *testing.T) {
	svc := &AddressBookService{}
	peer := &model.Peer{Id: "xyz", Os: "Linux Ubuntu 22.04"}
	ab := svc.FromPeer(peer)
	if ab.Alias != "" {
		t.Errorf("want empty alias, got %q", ab.Alias)
	}
	if ab.Platform != "Linux" {
		t.Errorf("want platform=Linux, got %q", ab.Platform)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 1: ownership check (service layer verification)
// ─────────────────────────────────────────────────────────────────────────────

// TestCheckCollectionOwner verifies that CheckCollectionOwner returns true
// only for the actual owner, and false for a different user or a missing collection.
func TestCheckCollectionOwner(t *testing.T) {
	setupABTestDB(t)
	svc := &AddressBookService{}

	col := &model.AddressBookCollection{UserId: 7, Name: "TeamA"}
	DB.Create(col)

	// Correct owner → true.
	if !svc.CheckCollectionOwner(7, col.Id) {
		t.Error("expected true for correct owner")
	}
	// Wrong user → false.
	if svc.CheckCollectionOwner(99, col.Id) {
		t.Error("expected false for wrong user")
	}
	// Non-existent collection → false (prevents information leak).
	if svc.CheckCollectionOwner(7, 999999) {
		t.Error("expected false for non-existent collection")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 2: transactional CreateBatch
// ─────────────────────────────────────────────────────────────────────────────

// TestCreateBatch_InsertsAllOnSuccess verifies normal path: all entries committed.
func TestCreateBatch_InsertsAllOnSuccess(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	const n = 10
	abs := make([]*model.AddressBook, n)
	for i := 0; i < n; i++ {
		abs[i] = &model.AddressBook{
			Id: fmt.Sprintf("peer%03d", i), UserId: 1, Tags: tagBytes(),
		}
	}
	if err := svc.CreateBatch(abs); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	var count int64
	db.Model(&model.AddressBook{}).Count(&count)
	if count != n {
		t.Errorf("want %d rows, got %d", n, count)
	}
}

// TestCreateBatch_RollbackOnFailure verifies that a mid-batch constraint
// violation causes the transaction to roll back, leaving ZERO new rows.
// A unique index is created at test time (in-memory SQLite only).
func TestCreateBatch_RollbackOnFailure(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	// Add a unique index so we can force a mid-batch conflict.
	db.Exec("CREATE UNIQUE INDEX uidx_test_ab ON address_books(user_id, id, collection_id)")

	// Pre-existing entry that will conflict with the 5th insert (peer004).
	existing := &model.AddressBook{Id: "peer004", UserId: 1, CollectionId: 0, Tags: tagBytes()}
	db.Create(existing)

	// Build a batch of 10; peer004 appears at index 4 → fails mid-batch.
	abs := make([]*model.AddressBook, 10)
	for i := 0; i < 10; i++ {
		abs[i] = &model.AddressBook{
			Id:           fmt.Sprintf("peer%03d", i),
			UserId:       1,
			CollectionId: 0,
			Tags:         tagBytes(),
		}
	}

	err := svc.CreateBatch(abs)
	if err == nil {
		t.Fatal("expected an error due to UNIQUE constraint, got nil")
	}

	// Only the pre-existing row should be present — no partial inserts.
	var count int64
	db.Model(&model.AddressBook{}).Count(&count)
	if count != 1 {
		t.Errorf("rollback failed: want 1 pre-existing row, got %d (partial inserts leaked)", count)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 3: 2-phase classification helpers
// ─────────────────────────────────────────────────────────────────────────────

// TestFindPeersByRowIds returns only the requested peers.
func TestFindPeersByRowIds(t *testing.T) {
	setupABTestDB(t)
	svc := &AddressBookService{}

	for i := 1; i <= 5; i++ {
		DB.Create(&model.Peer{Id: fmt.Sprintf("p%d", i), Hostname: fmt.Sprintf("host%d", i)})
	}

	var p1, p3, p5 model.Peer
	DB.Where("hostname = ?", "host1").First(&p1)
	DB.Where("hostname = ?", "host3").First(&p3)
	DB.Where("hostname = ?", "host5").First(&p5)

	got := svc.FindPeersByRowIds([]uint{p1.RowId, p3.RowId, p5.RowId})
	if len(got) != 3 {
		t.Errorf("want 3 peers, got %d", len(got))
	}
}

// TestInfoByUserIdAndIdAndCid_DetectsDuplicate confirms the duplicate-detection
// query used in phase 1 of the handler.
func TestInfoByUserIdAndIdAndCid_DetectsDuplicate(t *testing.T) {
	setupABTestDB(t)
	svc := &AddressBookService{}

	existing := &model.AddressBook{Id: "rustdesk1", UserId: 3, CollectionId: 0, Tags: tagBytes()}
	DB.Create(existing)

	// Same tripla → found.
	found := svc.InfoByUserIdAndIdAndCid(3, "rustdesk1", 0)
	if found.RowId == 0 {
		t.Error("expected to find existing AB entry")
	}
	// Different user → not found.
	notFound := svc.InfoByUserIdAndIdAndCid(99, "rustdesk1", 0)
	if notFound.RowId != 0 {
		t.Error("expected no result for different user")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Point 4: audit record
// ─────────────────────────────────────────────────────────────────────────────

// TestCreateBatchAudit_RecordIsPersisted verifies that every batch run
// produces exactly one audit row with the correct counters.
func TestCreateBatchAudit_RecordIsPersisted(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	if err := svc.CreateBatchAudit(&model.AuditAbBatch{
		AdminId:      10,
		UserId:       5,
		CollectionId: 2,
		Total:        7,
		Added:        4,
		Existing:     2,
		NotFound:     1,
		Failed:       0,
	}); err != nil {
		t.Fatalf("CreateBatchAudit: %v", err)
	}

	var record model.AuditAbBatch
	if result := db.First(&record); result.Error != nil {
		t.Fatalf("expected one audit record, got: %v", result.Error)
	}
	if record.AdminId != 10 || record.UserId != 5 || record.CollectionId != 2 {
		t.Errorf("audit ids mismatch: %+v", record)
	}
	if record.Total != 7 || record.Added != 4 || record.Existing != 2 || record.NotFound != 1 || record.Failed != 0 {
		t.Errorf("audit counters mismatch: %+v", record)
	}
}

// TestCreateBatchAudit_OneRecordPerRun verifies that multiple batch runs each
// produce a distinct audit record (not an upsert).
func TestCreateBatchAudit_OneRecordPerRun(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	for i := 0; i < 3; i++ {
		_ = svc.CreateBatchAudit(&model.AuditAbBatch{
			AdminId: 1, UserId: 2, Total: i + 1, Added: i + 1,
		})
	}
	var count int64
	db.Model(&model.AuditAbBatch{}).Count(&count)
	if count != 3 {
		t.Errorf("expected 3 audit records (one per run), got %d", count)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scale: 2 / 10 / 100 synthetic peers
// ─────────────────────────────────────────────────────────────────────────────

func runScaleBatch(t *testing.T, n int) {
	t.Helper()
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	// Seed n peers.
	peers := make([]*model.Peer, n)
	for i := 0; i < n; i++ {
		peers[i] = &model.Peer{
			Id:       fmt.Sprintf("scale-peer-%04d", i),
			Hostname: fmt.Sprintf("host-%04d", i),
			Os:       "Windows 11",
			Alias:    fmt.Sprintf("Alias %d", i),
		}
		DB.Create(peers[i])
	}

	// Reload to get assigned row_ids.
	var loaded []*model.Peer
	DB.Find(&loaded)

	// Build AB entries via FromPeer.
	abs := make([]*model.AddressBook, n)
	for i, p := range loaded {
		ab := svc.FromPeer(p)
		ab.Tags = tagBytes()
		abs[i] = ab
	}

	if err := svc.CreateBatch(abs); err != nil {
		t.Fatalf("CreateBatch(n=%d) failed: %v", n, err)
	}

	var count int64
	db.Model(&model.AddressBook{}).Count(&count)
	if count != int64(n) {
		t.Errorf("scale n=%d: want %d AB rows, got %d", n, n, count)
	}

	// Alias must be propagated for every entry.
	var zeroAlias int64
	db.Model(&model.AddressBook{}).Where("alias = ''").Count(&zeroAlias)
	if zeroAlias > 0 {
		t.Errorf("scale n=%d: %d entries have empty alias (alias copy bug)", n, zeroAlias)
	}
}

func TestScale_2Peers(t *testing.T)   { runScaleBatch(t, 2) }
func TestScale_10Peers(t *testing.T)  { runScaleBatch(t, 10) }
func TestScale_100Peers(t *testing.T) { runScaleBatch(t, 100) }

// TestUpdateFields_PreservesPinned ensures a regular admin edit (which does not
// carry the pinned flag) never clears an entry's pin.
func TestUpdateFields_PreservesPinned(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	// Seed a pinned entry.
	ab := &model.AddressBook{Id: "px", UserId: 1, CollectionId: 3, Pinned: true, Alias: "old", Tags: tagBytes()}
	db.Create(ab)

	// Simulate an admin edit: a form-built struct WITHOUT pinned (false),
	// addressing only the alias column.
	edit := &model.AddressBook{Alias: "new", Id: "px", UserId: 1, CollectionId: 3, Tags: tagBytes()}
	edit.RowId = ab.RowId
	if err := svc.UpdateFields(edit, []string{"alias"}); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}

	var reloaded model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&reloaded)
	if reloaded.Alias != "new" {
		t.Errorf("alias should update: got %q", reloaded.Alias)
	}
	if !reloaded.Pinned {
		t.Error("pinned must survive an admin edit that omits it")
	}
}

// TestUpdateFields_LeavesUnlistedColumnsAlone is the general regression guard
// for the Select("*") bug: an edit that addresses one column must not disturb
// any other, whatever the edit struct happens to carry in its zero-valued
// fields. Password and hash are the ones that actually hurt — both are peer
// credentials, and silently blanking them breaks connections rather than
// merely losing display text.
func TestUpdateFields_LeavesUnlistedColumnsAlone(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	ab := &model.AddressBook{
		Id: "px", UserId: 1, CollectionId: 3,
		Alias: "old-alias", Username: "operator", Hostname: "PC-01",
		Platform: "Windows", Password: "peer-pw", Hash: "peer-hash",
		RdpPort: "3389", RdpUsername: "rdpuser", LoginName: "login",
		Pinned: true, ForceAlwaysRelay: true, SameServer: true,
		Tags: tagBytes(),
	}
	db.Create(ab)

	// The edit struct is deliberately near-empty: it mimics a client that sent
	// only {"row_id":N,"alias":"new-alias"}. Under Select("*") every other
	// column would be overwritten with these zero values.
	edit := &model.AddressBook{Alias: "new-alias"}
	edit.RowId = ab.RowId
	if err := svc.UpdateFields(edit, []string{"alias"}); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}

	var got model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&got)

	if got.Alias != "new-alias" {
		t.Errorf("alias should have been written: got %q", got.Alias)
	}
	checks := []struct {
		column string
		want   interface{}
		got    interface{}
	}{
		{"password", "peer-pw", got.Password},
		{"hash", "peer-hash", got.Hash},
		{"id", "px", got.Id},
		{"username", "operator", got.Username},
		{"hostname", "PC-01", got.Hostname},
		{"platform", "Windows", got.Platform},
		{"rdp_port", "3389", got.RdpPort},
		{"rdp_username", "rdpuser", got.RdpUsername},
		{"login_name", "login", got.LoginName},
		{"user_id", uint(1), got.UserId},
		{"collection_id", uint(3), got.CollectionId},
		{"pinned", true, got.Pinned},
		{"force_always_relay", true, got.ForceAlwaysRelay},
		{"same_server", true, got.SameServer},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("column %q was clobbered by an edit that did not address it: want %v, got %v",
				ck.column, ck.want, ck.got)
		}
	}
}

// TestUpdateFields_EmptyColumnSetIsNoop verifies a request that addresses no
// writable column changes nothing and reports no error.
func TestUpdateFields_EmptyColumnSetIsNoop(t *testing.T) {
	db := setupABTestDB(t)
	svc := &AddressBookService{}

	ab := &model.AddressBook{Id: "px", UserId: 1, Alias: "keep", Password: "peer-pw", Tags: tagBytes()}
	db.Create(ab)

	edit := &model.AddressBook{Alias: "should-not-apply"}
	edit.RowId = ab.RowId
	if err := svc.UpdateFields(edit, nil); err != nil {
		t.Fatalf("empty column set should be a no-op, got error: %v", err)
	}

	var got model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&got)
	if got.Alias != "keep" || got.Password != "peer-pw" {
		t.Errorf("no-op update wrote anyway: alias=%q password=%q", got.Alias, got.Password)
	}
}
