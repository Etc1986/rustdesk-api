package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// These tests go through PeerClassificationBind — the real route wiring — and
// not through the handlers directly, because the defect they guard against
// lived in the router: the handler-level tests in http/controller/admin inject
// the user straight into the handler and could never see a missing middleware.

func setupPCRouterDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	l := log.New()
	l.SetOutput(io.Discard)
	global.Logger = l
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	global.Localizer = func(lang string) *i18n.Localizer {
		return i18n.NewLocalizer(bundle, "en")
	}
	global.ApiInitValidator()

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
	// Two devices nobody "owns" (user_id=0, as Lejianwen stores every peer).
	db.Create(&model.Peer{Id: "111111111", Hostname: "suc01-c1"})
	db.Create(&model.Peer{Id: "222222222", Hostname: "suc02-c1"})
	return db
}

func pcTestUser(id uint, admin bool) *model.User {
	u := &model.User{}
	u.Id = id
	u.IsAdmin = &admin
	return u
}

// pcTestEngine mounts the classification routes exactly as Init does, with a
// stand-in for BackendUserAuth that places the given user in the context.
func pcTestEngine(u *model.User) *gin.Engine {
	r := gin.New()
	g := r.Group("/api/admin")
	g.Use(func(c *gin.Context) {
		c.Set("curUser", u)
		c.Next()
	})
	PeerClassificationBind(g)
	return r
}

func pcCall(t *testing.T, r *gin.Engine, method, path, body string) map[string]interface{} {
	t.Helper()
	var rd io.Reader
	if method == http.MethodPost {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s: unmarshal %q: %v", method, path, w.Body.String(), err)
	}
	return resp
}

// TestPeerClassificationRoutes_RejectNonAdmin: every route of the group must
// refuse a logged-in non-admin with 403, before the handler runs.
func TestPeerClassificationRoutes_RejectNonAdmin(t *testing.T) {
	db := setupPCRouterDB(t)
	r := pcTestEngine(pcTestUser(7, false))

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/peer_classification/rule/list", ""},
		{http.MethodPost, "/api/admin/peer_classification/rule/create", `{"matcher_type":"prefix","pattern":"suc","priority":1}`},
		{http.MethodPost, "/api/admin/peer_classification/rule/update", `{"id":1,"matcher_type":"prefix","pattern":"suc"}`},
		{http.MethodPost, "/api/admin/peer_classification/rule/delete", `{"id":1}`},
		{http.MethodPost, "/api/admin/peer_classification/pin", `{"row_id":1,"pinned":true}`},
		{http.MethodPost, "/api/admin/peer_classification/simulate", `{}`},
		{http.MethodPost, "/api/admin/peer_classification/apply", `{}`},
		{http.MethodPost, "/api/admin/peer_classification/undo", `{}`},
		{http.MethodGet, "/api/admin/peer_classification/last-run", ""},
	}
	for _, rt := range routes {
		resp := pcCall(t, r, rt.method, rt.path, rt.body)
		if code, _ := resp["code"].(float64); code != 403 {
			t.Errorf("%s %s: non-admin got code %v, want 403 (resp=%v)", rt.method, rt.path, resp["code"], resp)
		}
		if resp["data"] != nil {
			t.Errorf("%s %s: non-admin received data: %v", rt.method, rt.path, resp["data"])
		}
	}

	// Nothing may have been written by the refused calls.
	var rules, entries int64
	db.Model(&model.PeerClassificationRule{}).Count(&rules)
	db.Model(&model.AddressBook{}).Count(&entries)
	if rules != 0 || entries != 0 {
		t.Errorf("refused calls wrote data: rules=%d address_books=%d", rules, entries)
	}
}

// TestPeerClassificationRoutes_SimulateNoLongerLeaksFleet pins the concrete
// exposure: a non-admin with ZERO rules used to get every peer back.
func TestPeerClassificationRoutes_SimulateNoLongerLeaksFleet(t *testing.T) {
	setupPCRouterDB(t)
	resp := pcCall(t, pcTestEngine(pcTestUser(7, false)), http.MethodPost,
		"/api/admin/peer_classification/simulate", `{}`)
	if b, _ := json.Marshal(resp); bytes.Contains(b, []byte("111111111")) || bytes.Contains(b, []byte("suc02-c1")) {
		t.Fatalf("non-admin simulate still exposes peers: %s", b)
	}
}

// TestPeerClassificationRoutes_AdminStillWorks: the guard must not break the
// feature for administrators.
func TestPeerClassificationRoutes_AdminStillWorks(t *testing.T) {
	setupPCRouterDB(t)
	resp := pcCall(t, pcTestEngine(pcTestUser(1, true)), http.MethodPost,
		"/api/admin/peer_classification/simulate", `{}`)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("admin simulate: code %v, want 0 (resp=%v)", resp["code"], resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	if total, _ := data["total"].(float64); total != 2 {
		t.Errorf("admin simulate: total %v, want 2", data["total"])
	}
}
