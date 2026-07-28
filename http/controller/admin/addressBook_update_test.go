package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/model"
)

// itoa renders a row id for inlining into a raw JSON body.
func itoa(v uint) string { return strconv.FormatUint(uint64(v), 10) }

// updateRouter exposes the admin address-book Update handler.
func updateRouter(adminUser *model.User) *gin.Engine {
	r := gin.New()
	ct := &AddressBook{}
	r.POST("/update", func(c *gin.Context) {
		if adminUser != nil {
			c.Set("curUser", adminUser)
		}
		ct.Update(c)
	})
	return r
}

// postUpdateRaw posts a raw JSON body, so a test can control exactly which keys
// are present — which is the whole point of these regressions.
func postUpdateRaw(t *testing.T, router *gin.Engine, body string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	return resp
}

// TestUpdate_OmittedPasswordIsPreserved is the end-to-end regression for the
// reported bug: an admin edit that does not carry `password` used to blank it,
// because Select("*") wrote every column whether or not the caller sent it.
// This is not hypothetical — the UI happens to resend every key, so the bug
// only fires for API clients, which is exactly where it goes unnoticed.
func TestUpdate_OmittedPasswordIsPreserved(t *testing.T) {
	db := setupHandlerDB(t)

	ab := &model.AddressBook{
		Id: "pc-1", UserId: 3, Password: "peer-pw", Hash: "peer-hash",
		Alias: "old", Username: "operator", Hostname: "PC-01", Tags: []byte(`[]`),
	}
	db.Create(ab)

	router := updateRouter(makeUser(10))
	resp := postUpdateRaw(t, router, `{"row_id":`+itoa(ab.RowId)+`,"user_id":3,"id":"pc-1","alias":"new"}`)
	if respCode(resp) != 0 {
		t.Fatalf("update failed: %v", resp)
	}

	var got model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&got)
	if got.Alias != "new" {
		t.Errorf("alias should have changed: got %q", got.Alias)
	}
	if got.Password != "peer-pw" {
		t.Errorf("password was erased by an edit that did not mention it: got %q", got.Password)
	}
	if got.Hash != "peer-hash" {
		t.Errorf("hash was erased by an edit that did not mention it: got %q", got.Hash)
	}
	if got.Username != "operator" || got.Hostname != "PC-01" {
		t.Errorf("unrelated fields were clobbered: username=%q hostname=%q", got.Username, got.Hostname)
	}
}

// TestUpdate_ExplicitPasswordIsWritten guards the other direction: the fix must
// not make the field unwritable. Sending it, including sending it empty on
// purpose, still takes effect.
func TestUpdate_ExplicitPasswordIsWritten(t *testing.T) {
	db := setupHandlerDB(t)

	ab := &model.AddressBook{Id: "pc-1", UserId: 3, Password: "old-pw", Tags: []byte(`[]`)}
	db.Create(ab)

	router := updateRouter(makeUser(10))

	resp := postUpdateRaw(t, router, `{"row_id":`+itoa(ab.RowId)+`,"user_id":3,"id":"pc-1","password":"new-pw"}`)
	if respCode(resp) != 0 {
		t.Fatalf("update failed: %v", resp)
	}
	var got model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&got)
	if got.Password != "new-pw" {
		t.Errorf("explicit password was not written: got %q", got.Password)
	}

	resp = postUpdateRaw(t, router, `{"row_id":`+itoa(ab.RowId)+`,"user_id":3,"id":"pc-1","password":""}`)
	if respCode(resp) != 0 {
		t.Fatalf("clearing update failed: %v", resp)
	}
	db.Where("row_id = ?", ab.RowId).First(&got)
	if got.Password != "" {
		t.Errorf("deliberately cleared password should be empty: got %q", got.Password)
	}
}

// TestUpdate_PinnedStaysUnwritable verifies the pin flag cannot be flipped
// through the generic edit endpoint even when named explicitly — it belongs to
// the classification pin endpoint. Under the old blocklist this held only
// because pinned was omitted; now it holds because it is not on the allowlist.
func TestUpdate_PinnedStaysUnwritable(t *testing.T) {
	db := setupHandlerDB(t)

	ab := &model.AddressBook{Id: "pc-1", UserId: 3, Pinned: true, Tags: []byte(`[]`)}
	db.Create(ab)

	router := updateRouter(makeUser(10))
	resp := postUpdateRaw(t, router, `{"row_id":`+itoa(ab.RowId)+`,"user_id":3,"id":"pc-1","pinned":false,"alias":"x"}`)
	if respCode(resp) != 0 {
		t.Fatalf("update failed: %v", resp)
	}

	var got model.AddressBook
	db.Where("row_id = ?", ab.RowId).First(&got)
	if !got.Pinned {
		t.Error("pinned was cleared through the generic admin edit endpoint")
	}
	if got.Alias != "x" {
		t.Errorf("the legitimate part of the edit should still apply: alias=%q", got.Alias)
	}
}
