package admin

import (
	"reflect"
	"testing"
)

// TestBindAddressBookUpdate_OnlyReturnsColumnsActuallySent is the core of the
// Select("*") fix: the write-set must come from the request body, not from the
// model. A body naming one field must yield exactly one column.
func TestBindAddressBookUpdate_OnlyReturnsColumnsActuallySent(t *testing.T) {
	f, cols, err := BindAddressBookUpdate([]byte(`{"row_id":7,"alias":"new"}`))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if f.RowId != 7 || f.Alias != "new" {
		t.Errorf("form decoded wrong: row_id=%d alias=%q", f.RowId, f.Alias)
	}
	if !reflect.DeepEqual(cols, []string{"alias"}) {
		t.Errorf("want exactly [alias], got %v", cols)
	}
}

// TestBindAddressBookUpdate_MapsJsonKeysToColumns checks the camelCase JSON
// keys reach their snake_case columns — a mismatch here would silently drop
// the field from the write-set instead of erroring.
func TestBindAddressBookUpdate_MapsJsonKeysToColumns(t *testing.T) {
	body := []byte(`{
		"row_id":1,"id":"px","username":"u","password":"p","hostname":"h",
		"alias":"a","platform":"Windows","tags":["x"],"hash":"hh","user_id":2,
		"forceAlwaysRelay":true,"rdpPort":"3389","rdpUsername":"ru",
		"online":true,"loginName":"ln","sameServer":true,"collection_id":3
	}`)
	_, cols, err := BindAddressBookUpdate(body)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	want := []string{
		"alias", "collection_id", "force_always_relay", "hash", "hostname", "id",
		"login_name", "online", "password", "platform", "rdp_port", "rdp_username",
		"same_server", "tags", "user_id", "username",
	}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("column mapping drifted:\n want %v\n  got %v", want, cols)
	}
}

// TestBindAddressBookUpdate_NeverExposesProtectedColumns verifies that columns
// outside the allowlist cannot be written even when the caller names them
// explicitly. row_id identifies the row being edited, pinned belongs to the
// classification pin endpoint, and the timestamps belong to GORM.
func TestBindAddressBookUpdate_NeverExposesProtectedColumns(t *testing.T) {
	body := []byte(`{
		"row_id":7,"alias":"new","pinned":false,
		"created_at":"2020-01-01 00:00:00","updated_at":"2020-01-01 00:00:00",
		"collection":{"id":9},"user_ids":[1,2],"totally_unknown":"x"
	}`)
	_, cols, err := BindAddressBookUpdate(body)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !reflect.DeepEqual(cols, []string{"alias"}) {
		t.Errorf("protected or unknown keys leaked into the write-set: %v", cols)
	}
}

// TestBindAddressBookUpdate_DistinguishesOmittedFromEmpty is the precise
// distinction the old code could not make. An absent key must stay out of the
// write-set; an explicit empty string must go in, because clearing a field on
// purpose is a legitimate edit.
func TestBindAddressBookUpdate_DistinguishesOmittedFromEmpty(t *testing.T) {
	_, omitted, err := BindAddressBookUpdate([]byte(`{"row_id":7,"alias":"a"}`))
	if err != nil {
		t.Fatalf("bind omitted: %v", err)
	}
	for _, c := range omitted {
		if c == "password" {
			t.Fatal("password was omitted from the body but ended up in the write-set")
		}
	}

	_, explicit, err := BindAddressBookUpdate([]byte(`{"row_id":7,"password":""}`))
	if err != nil {
		t.Fatalf("bind explicit: %v", err)
	}
	if !reflect.DeepEqual(explicit, []string{"password"}) {
		t.Errorf("an explicit empty password must still be written, got %v", explicit)
	}
}

// TestBindAddressBookUpdate_RejectsMalformedBody keeps the decode failure path
// an error rather than a silent empty write-set.
func TestBindAddressBookUpdate_RejectsMalformedBody(t *testing.T) {
	if _, _, err := BindAddressBookUpdate([]byte(`{"row_id":`)); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}
