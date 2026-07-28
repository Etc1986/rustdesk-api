package admin

import (
	"encoding/json"
	_ "encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/global"
	"github.com/lejianwen/rustdesk-api/v2/http/request/admin"
	"github.com/lejianwen/rustdesk-api/v2/http/response"
	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/service"
	"gorm.io/gorm"
	"strconv"
)

type AddressBook struct {
}

// Detail 地址簿
// @Tags 地址簿
// @Summary 地址簿详情
// @Description 地址簿详情
// @Accept  json
// @Produce  json
// @Param id path int true "ID"
// @Success 200 {object} response.Response{data=model.AddressBook}
// @Failure 500 {object} response.Response
// @Router /admin/address_book/detail/{id} [get]
// @Security token
func (ct *AddressBook) Detail(c *gin.Context) {
	id := c.Param("id")
	iid, _ := strconv.Atoi(id)
	t := service.AllService.AddressBookService.InfoByRowId(uint(iid))
	if t.RowId > 0 {
		response.Success(c, t)
		return
	}
	response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
	return
}

// Create 创建地址簿
// @Tags 地址簿
// @Summary 创建地址簿
// @Description 创建地址簿
// @Accept  json
// @Produce  json
// @Param body body admin.AddressBookForm true "地址簿信息"
// @Success 200 {object} response.Response{data=model.AddressBook}
// @Failure 500 {object} response.Response
// @Router /admin/address_book/create [post]
// @Security token
func (ct *AddressBook) Create(c *gin.Context) {
	f := &admin.AddressBookForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	errList := global.Validator.ValidStruct(c, f)
	if len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	t := f.ToAddressBook()
	if t.UserId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}
	if t.CollectionId > 0 && !service.AllService.AddressBookService.CheckCollectionOwner(t.UserId, t.CollectionId) {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}

	ex := service.AllService.AddressBookService.InfoByUserIdAndIdAndCid(t.UserId, t.Id, t.CollectionId)
	if ex.RowId > 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemExists"))
		return
	}

	err := service.AllService.AddressBookService.Create(t)
	if err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// BatchCreate 批量创建地址簿
// @Tags 地址簿
// @Summary 批量创建地址簿
// @Description 批量创建地址簿
// @Accept  json
// @Produce  json
// @Param body body admin.AddressBookForm true "地址簿信息"
// @Success 200 {object} response.Response{data=model.AddressBook}
// @Failure 500 {object} response.Response
// @Router /admin/address_book/batchCreate [post]
// @Security token
func (ct *AddressBook) BatchCreate(c *gin.Context) {
	f := &admin.AddressBookForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	errList := global.Validator.ValidStruct(c, f)
	if len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	ul := len(f.UserIds)

	if ul == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}
	if ul > 1 {
		//多用户置空标签
		f.Tags = []string{}
		//多用户只能创建到默认地址簿
		f.CollectionId = 0
	}

	//创建标签
	/*for _, fu := range f.UserIds {
		if fu == 0 {
			continue
		}
		for _, ft := range f.Tags {
			exTag := service.AllService.TagService.InfoByUserIdAndNameAndCollectionId(fu, ft, 0)
			if exTag.Id == 0 {
				service.AllService.TagService.Create(&model.Tag{
					UserId: fu,
					Name:   ft,
				})
			}
		}
	}*/
	ts := f.ToAddressBooks()
	for _, t := range ts {
		if t.UserId == 0 {
			continue
		}
		ex := service.AllService.AddressBookService.InfoByUserIdAndIdAndCid(t.UserId, t.Id, t.CollectionId)
		if ex.RowId == 0 {
			service.AllService.AddressBookService.Create(t)
		}
	}

	response.Success(c, nil)
}

// List 列表
// @Tags 地址簿
// @Summary 地址簿列表
// @Description 地址簿列表
// @Accept  json
// @Produce  json
// @Param page query int false "页码"
// @Param page_size query int false "页大小"
// @Param user_id query int false "用户id"
// @Param is_my query int false "是否是我的"
// @Success 200 {object} response.Response{data=model.AddressBookList}
// @Failure 500 {object} response.Response
// @Router /admin/address_book/list [get]
// @Security token
func (ct *AddressBook) List(c *gin.Context) {
	query := &admin.AddressBookQuery{}
	if err := c.ShouldBindQuery(query); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	res := service.AllService.AddressBookService.List(query.Page, query.PageSize, func(tx *gorm.DB) {
		tx.Preload("Collection", func(txc *gorm.DB) *gorm.DB {
			return txc.Select("id,name")
		})
		if query.Id != "" {
			tx.Where("id like ?", "%"+query.Id+"%")
		}
		if query.UserId > 0 {
			tx.Where("user_id = ?", query.UserId)
		}
		if query.Username != "" {
			tx.Where("username like ?", "%"+query.Username+"%")
		}
		if query.Hostname != "" {
			tx.Where("hostname like ?", "%"+query.Hostname+"%")
		}
		if query.CollectionId != nil && *query.CollectionId >= 0 {
			tx.Where("collection_id = ?", query.CollectionId)
		}
	})

	abCIds := make([]uint, 0)
	for _, ab := range res.AddressBooks {
		abCIds = append(abCIds, ab.CollectionId)
	}
	response.Success(c, res)
}

// Update 编辑
// @Tags 地址簿
// @Summary 地址簿编辑
// @Description 地址簿编辑
// @Accept  json
// @Produce  json
// @Param body body admin.AddressBookForm true "地址簿信息"
// @Success 200 {object} response.Response{data=model.AddressBook}
// @Failure 500 {object} response.Response
// @Router /admin/address_book/update [post]
// @Security token
func (ct *AddressBook) Update(c *gin.Context) {
	raw, err := c.GetRawData()
	if err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	// Bind the form AND the set of columns the request actually addresses, so
	// fields the caller omitted keep their stored value instead of being reset.
	f, columns, err := admin.BindAddressBookUpdate(raw)
	if err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	errList := global.Validator.ValidStruct(c, f)
	if len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	if f.RowId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	ex := service.AllService.AddressBookService.InfoByRowId(f.RowId)
	if ex.RowId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	t := f.ToAddressBook()
	if t.CollectionId > 0 && !service.AllService.AddressBookService.CheckCollectionOwner(t.UserId, t.CollectionId) {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}
	if err := service.AllService.AddressBookService.UpdateFields(t, columns); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// Delete 删除
// @Tags 地址簿
// @Summary 地址簿删除
// @Description 地址簿删除
// @Accept  json
// @Produce  json
// @Param body body admin.AddressBookForm true "地址簿信息"
// @Success 200 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /admin/address_book/delete [post]
// @Security token
func (ct *AddressBook) Delete(c *gin.Context) {
	f := &admin.AddressBookForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	id := f.RowId
	errList := global.Validator.ValidVar(c, id, "required,gt=0")
	if len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	t := service.AllService.AddressBookService.InfoByRowId(f.RowId)
	if t.RowId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	err := service.AllService.AddressBookService.Delete(t)
	if err == nil {
		response.Success(c, nil)
		return
	}
	response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
}

// ShareByWebClient
// @Tags 地址簿
// @Summary 地址簿分享
// @Description 地址簿分享
// @Accept  json
// @Produce  json
// @Param body body admin.ShareByWebClientForm true "地址簿信息"
// @Success 200 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /admin/address_book/share [post]
// @Security token
func (ct *AddressBook) ShareByWebClient(c *gin.Context) {
	f := &admin.ShareByWebClientForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	errList := global.Validator.ValidStruct(c, f)
	if len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}

	u := service.AllService.UserService.CurUser(c)
	ab := service.AllService.AddressBookService.InfoByUserIdAndId(u.Id, f.Id)
	if ab.RowId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	m := f.ToShareRecord()
	m.UserId = u.Id
	err := service.AllService.AddressBookService.ShareByWebClient(m)
	if err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, &gin.H{
		"share_token": m.ShareToken,
	})
}

func (ct *AddressBook) BatchCreateFromPeers(c *gin.Context) {
	f := &admin.BatchCreateFromPeersForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}

	if f.UserId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}

	// Ownership check: collection must belong to the target user_id.
	// CheckCollectionOwner returns false both when collection doesn't exist
	// and when it exists but belongs to a different user — intentionally opaque.
	if f.CollectionId != 0 {
		if !service.AllService.AddressBookService.CheckCollectionOwner(f.UserId, f.CollectionId) {
			response.Fail(c, 101, response.TranslateMsg(c, "NoAccess"))
			return
		}
	}

	if len(f.PeerIds) == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}

	tags, _ := json.Marshal(f.Tags)

	// Phase 1 — Classification (read-only, no DB writes).
	peers := service.AllService.AddressBookService.FindPeersByRowIds(f.PeerIds)
	peerMap := make(map[uint]*model.Peer, len(peers))
	for _, p := range peers {
		peerMap[p.RowId] = p
	}

	type PeerResult struct {
		PeerId uint   `json:"peer_id"`
		Status string `json:"status"` // added | existing | not_found | failed
	}
	results := make([]PeerResult, 0, len(f.PeerIds))
	pendingABs := make([]*model.AddressBook, 0)
	pendingIdxs := make([]int, 0)
	notFoundCount, existingCount := 0, 0

	for _, pid := range f.PeerIds {
		peer, ok := peerMap[pid]
		if !ok {
			results = append(results, PeerResult{PeerId: pid, Status: "not_found"})
			notFoundCount++
			continue
		}
		ab := service.AllService.AddressBookService.FromPeer(peer)
		ab.Tags = tags
		ab.CollectionId = f.CollectionId
		ab.UserId = f.UserId
		ex := service.AllService.AddressBookService.InfoByUserIdAndIdAndCid(f.UserId, ab.Id, ab.CollectionId)
		if ex.RowId != 0 {
			results = append(results, PeerResult{PeerId: pid, Status: "existing"})
			existingCount++
			continue
		}
		pendingIdxs = append(pendingIdxs, len(results))
		results = append(results, PeerResult{PeerId: pid, Status: "pending"})
		pendingABs = append(pendingABs, ab)
	}

	// Phase 2 — Transactional writes (all-or-nothing for the pending set).
	addedCount, failedCount := 0, 0
	if len(pendingABs) > 0 {
		if err := service.AllService.AddressBookService.CreateBatch(pendingABs); err != nil {
			failedCount = len(pendingABs)
			for _, idx := range pendingIdxs {
				results[idx].Status = "failed"
			}
		} else {
			addedCount = len(pendingABs)
			for _, idx := range pendingIdxs {
				results[idx].Status = "added"
			}
		}
	}

	// Phase 3 — Audit (best-effort; never blocks the response).
	adminUser := service.AllService.UserService.CurUser(c)
	_ = service.AllService.AddressBookService.CreateBatchAudit(&model.AuditAbBatch{
		AdminId:      adminUser.Id,
		UserId:       f.UserId,
		CollectionId: f.CollectionId,
		Total:        len(f.PeerIds),
		Added:        addedCount,
		Existing:     existingCount,
		NotFound:     notFoundCount,
		Failed:       failedCount,
	})

	response.Success(c, gin.H{
		"total":     len(f.PeerIds),
		"added":     addedCount,
		"existing":  existingCount,
		"not_found": notFoundCount,
		"failed":    failedCount,
		"results":   results,
	})
}

// BatchSetPassword 批量设置密码
// @Tags 地址簿
// @Summary 批量设置地址簿密码
// @Description 批量设置地址簿密码
// @Accept json
// @Produce json
// @Param body body admin.BatchSetPasswordForm true "批量设置密码"
// @Success 200 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /admin/address_book/batchSetPassword [post]
// @Security token
func (ct *AddressBook) BatchSetPassword(c *gin.Context) {
	f := &admin.BatchSetPasswordForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	if errList := global.Validator.ValidStruct(c, f); len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	if f.Password == "" {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}

	// The target set is addressed by row ids XOR by collection. Accepting both
	// would leave it undefined which one wins on a bulk credential write, and
	// accepting neither would silently target nothing.
	byRows, byCollection := len(f.RowIds) > 0, f.CollectionId != 0
	if byRows == byCollection {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}

	// Phase 1 — Resolve the target set (read-only, no DB writes).
	type EntryResult struct {
		RowId  uint   `json:"row_id"`
		Status string `json:"status"` // updated | unchanged | forbidden | not_found | failed
	}
	results := make([]EntryResult, 0)
	pendingIds := make([]uint, 0)
	pendingIdxs := make([]int, 0)
	skippedCount, notFoundCount := 0, 0
	auditUserId, auditCollectionId := f.UserId, f.CollectionId

	// classify decides the fate of one resolved entry, shared by both modes.
	classify := func(ab *model.AddressBook) {
		idx := len(results)
		switch {
		case f.UserId != 0 && ab.UserId != f.UserId:
			// Scope guard was supplied and this entry falls outside it.
			results = append(results, EntryResult{RowId: ab.RowId, Status: "forbidden"})
			skippedCount++
		case ab.Password == f.Password:
			// Already correct — writing it again would only churn updated_at.
			results = append(results, EntryResult{RowId: ab.RowId, Status: "unchanged"})
			skippedCount++
		default:
			results = append(results, EntryResult{RowId: ab.RowId, Status: "pending"})
			pendingIdxs = append(pendingIdxs, idx)
			pendingIds = append(pendingIds, ab.RowId)
		}
	}

	if byCollection {
		// Ownership: the collection must exist, and — when a scope guard was
		// supplied — belong to that user. CheckCollectionOwner is intentionally
		// opaque: it answers false both for "missing" and "someone else's".
		collection := service.AllService.AddressBookService.CollectionInfoById(f.CollectionId)
		if collection.Id == 0 {
			response.Fail(c, 101, response.TranslateMsg(c, "NoAccess"))
			return
		}
		if f.UserId != 0 && !service.AllService.AddressBookService.CheckCollectionOwner(f.UserId, f.CollectionId) {
			response.Fail(c, 101, response.TranslateMsg(c, "NoAccess"))
			return
		}
		// With no explicit guard, the collection's owner is the audited subject.
		if auditUserId == 0 {
			auditUserId = collection.UserId
		}
		for _, ab := range service.AllService.AddressBookService.FindByCollectionId(f.CollectionId) {
			classify(ab)
		}
	} else {
		found := service.AllService.AddressBookService.FindByRowIds(f.RowIds)
		byId := make(map[uint]*model.AddressBook, len(found))
		for _, ab := range found {
			byId[ab.RowId] = ab
		}
		// Iterate the request order, not the query order, so results[] lines up
		// with what the caller sent and missing ids are reported rather than
		// dropped.
		for _, rid := range f.RowIds {
			ab, ok := byId[rid]
			if !ok {
				results = append(results, EntryResult{RowId: rid, Status: "not_found"})
				notFoundCount++
				continue
			}
			classify(ab)
		}
	}

	// Phase 2 — Transactional write (all-or-nothing for the pending set).
	updatedCount, failedCount := 0, 0
	if len(pendingIds) > 0 {
		if err := service.AllService.AddressBookService.BatchSetPassword(pendingIds, f.Password); err != nil {
			failedCount = len(pendingIds)
			for _, idx := range pendingIdxs {
				results[idx].Status = "failed"
			}
		} else {
			updatedCount = len(pendingIds)
			for _, idx := range pendingIdxs {
				results[idx].Status = "updated"
			}
		}
	}

	// Phase 3 — Audit (best-effort; never blocks the response, never records
	// the password itself — see model.AuditAbPassword).
	// CurUser returns nil when the context carries no user. AdminPrivilege
	// makes that unreachable in production, but the write has already been
	// committed by this point: a nil deref here would turn a successful
	// assignment into a 500 and strand the caller with no idea it went through.
	adminId := uint(0)
	if adminUser := service.AllService.UserService.CurUser(c); adminUser != nil {
		adminId = adminUser.Id
	}
	_ = service.AllService.AddressBookService.CreatePasswordAudit(&model.AuditAbPassword{
		AdminId:      adminId,
		UserId:       auditUserId,
		CollectionId: auditCollectionId,
		Total:        len(results),
		Updated:      updatedCount,
		Skipped:      skippedCount,
		NotFound:     notFoundCount,
		Failed:       failedCount,
	})

	response.Success(c, gin.H{
		"total":     len(results),
		"updated":   updatedCount,
		"skipped":   skippedCount,
		"not_found": notFoundCount,
		"failed":    failedCount,
		"results":   results,
	})
}
