package admin

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/lejianwen/rustdesk-api/v2/global"
	"github.com/lejianwen/rustdesk-api/v2/http/request/admin"
	"github.com/lejianwen/rustdesk-api/v2/http/response"
	"github.com/lejianwen/rustdesk-api/v2/model"
	"github.com/lejianwen/rustdesk-api/v2/service"
	"gorm.io/gorm"
)

// PeerClassificationRule is the admin controller for the peer-classification
// rule engine. Every operation is scoped to the current backend user
// (multi-tenant): a user only ever sees, edits and runs their own rules against
// their own peers.
type PeerClassificationRule struct {
}

// List returns the current user's classification rules.
// @Router /admin/peer_classification/rule/list [get]
func (ct *PeerClassificationRule) List(c *gin.Context) {
	query := &admin.PeerClassificationRuleQuery{}
	if err := c.ShouldBindQuery(query); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	u := service.AllService.UserService.CurUser(c)
	res := service.AllService.PeerClassificationService.ListRules(query.Page, query.PageSize, func(tx *gorm.DB) {
		tx.Where("user_id = ?", u.Id)
		if query.Active != nil {
			tx.Where("active = ?", *query.Active == 1)
		}
	})
	response.Success(c, res)
}

// bindAndValidate binds the rule body, runs struct validation, forces the owner
// to the current user, normalises tags, and runs ownership + ambiguity checks.
// Returns (rule, ok). On !ok it has already written the failure response.
func (ct *PeerClassificationRule) bindAndValidate(c *gin.Context, requireId bool) (*model.PeerClassificationRule, bool) {
	f := &model.PeerClassificationRule{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return nil, false
	}
	if errList := global.Validator.ValidStruct(c, f); len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return nil, false
	}
	if requireId && f.Id == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return nil, false
	}

	u := service.AllService.UserService.CurUser(c)
	f.UserId = u.Id

	// Normalise tags so the NOT NULL column always has a valid JSON array.
	if len(service.DecodeTags(f.TargetTags)) == 0 {
		f.TargetTags = service.EncodeTags(nil)
	}

	// Target collection (if any) must belong to the current user.
	if f.TargetCollectionId != nil {
		if !service.AllService.AddressBookService.CheckCollectionOwner(u.Id, *f.TargetCollectionId) {
			response.Fail(c, 101, response.TranslateMsg(c, "NoAccess"))
			return nil, false
		}
	}

	// On update, ensure the rule exists and belongs to the current user.
	if requireId {
		ex := service.AllService.PeerClassificationService.InfoById(f.Id)
		if ex.Id == 0 || ex.UserId != u.Id {
			response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
			return nil, false
		}
	}

	// Hard reject exact (user, matcher_type, pattern) duplicates.
	if dup := service.AllService.PeerClassificationService.FindDuplicate(u.Id, f.MatcherType, f.Pattern, f.Id); dup != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemExists"))
		return nil, false
	}

	// Reject same-priority ambiguity over target collection.
	if conflict := service.AllService.PeerClassificationService.FindConflict(f); conflict != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "PeerClassificationRuleConflict"))
		return nil, false
	}

	return f, true
}

// Create creates a rule.
// @Router /admin/peer_classification/rule/create [post]
func (ct *PeerClassificationRule) Create(c *gin.Context) {
	f, ok := ct.bindAndValidate(c, false)
	if !ok {
		return
	}
	if err := service.AllService.PeerClassificationService.CreateRule(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// Update edits a rule.
// @Router /admin/peer_classification/rule/update [post]
func (ct *PeerClassificationRule) Update(c *gin.Context) {
	f, ok := ct.bindAndValidate(c, true)
	if !ok {
		return
	}
	if err := service.AllService.PeerClassificationService.UpdateRule(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// Delete removes a rule owned by the current user.
// @Router /admin/peer_classification/rule/delete [post]
func (ct *PeerClassificationRule) Delete(c *gin.Context) {
	f := &model.PeerClassificationRule{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	if f.Id == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError"))
		return
	}
	u := service.AllService.UserService.CurUser(c)
	ex := service.AllService.PeerClassificationService.InfoById(f.Id)
	if ex.Id == 0 || ex.UserId != u.Id {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	if err := service.AllService.PeerClassificationService.DeleteRule(ex); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// Pin sets or clears the pinned flag on an address-book entry. A user may pin
// their own entries; pinning another user's entry requires admin privilege.
// @Router /admin/peer_classification/pin [post]
func (ct *PeerClassificationRule) Pin(c *gin.Context) {
	f := &admin.PinForm{}
	if err := c.ShouldBindJSON(f); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "ParamsError")+err.Error())
		return
	}
	if errList := global.Validator.ValidStruct(c, f); len(errList) > 0 {
		response.Fail(c, 101, errList[0])
		return
	}
	u := service.AllService.UserService.CurUser(c)
	ab := service.AllService.AddressBookService.InfoByRowId(f.RowId)
	if ab.RowId == 0 {
		response.Fail(c, 101, response.TranslateMsg(c, "ItemNotFound"))
		return
	}
	// Scope: own entry, or an admin acting on someone else's.
	isAdmin := u != nil && u.IsAdmin != nil && *u.IsAdmin
	if ab.UserId != u.Id && !isAdmin {
		response.Fail(c, 101, response.TranslateMsg(c, "NoAccess"))
		return
	}
	if err := service.AllService.PeerClassificationService.SetPinned(f.RowId, f.Pinned); err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, nil)
}

// Simulate computes the full classification plan for the current user's peers
// WITHOUT writing anything (dry-run). Re-evaluates every peer, including those
// already in the address book.
// @Router /admin/peer_classification/simulate [post]
func (ct *PeerClassificationRule) Simulate(c *gin.Context) {
	u := service.AllService.UserService.CurUser(c)
	results, summary := service.AllService.PeerClassificationService.BuildPlan(u.Id)
	response.Success(c, classificationPayload(false, summary, results))
}

// Apply computes the plan and applies it transactionally, then returns the same
// detailed result as Simulate plus applied:true.
// @Router /admin/peer_classification/apply [post]
func (ct *PeerClassificationRule) Apply(c *gin.Context) {
	u := service.AllService.UserService.CurUser(c)
	results, summary, err := service.AllService.PeerClassificationService.Apply(u.Id, u.Id)
	if err != nil {
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, classificationPayload(true, summary, results))
}

// Undo reverts the current user's most recent apply run.
// @Router /admin/peer_classification/undo [post]
func (ct *PeerClassificationRule) Undo(c *gin.Context) {
	u := service.AllService.UserService.CurUser(c)
	res, err := service.AllService.PeerClassificationService.Undo(u.Id, u.Id)
	if err != nil {
		if errors.Is(err, service.ErrNoRun) || errors.Is(err, service.ErrAlreadyReverted) {
			response.Fail(c, 101, response.TranslateMsg(c, err.Error()))
			return
		}
		response.Fail(c, 101, response.TranslateMsg(c, "OperationFailed")+err.Error())
		return
	}
	response.Success(c, res)
}

// LastRun reports the current user's most recent apply run and whether it can
// still be undone.
// @Router /admin/peer_classification/last-run [get]
func (ct *PeerClassificationRule) LastRun(c *gin.Context) {
	u := service.AllService.UserService.CurUser(c)
	info := service.AllService.PeerClassificationService.LastRun(u.Id)
	payload := gin.H{
		"has_run":    info.Run != nil,
		"reversible": info.Reversible,
		"reason":     info.Reason,
		"run":        nil,
	}
	if info.Run != nil {
		payload["run"] = gin.H{
			"id":             info.Run.Id,
			"created_at":     info.Run.CreatedAt,
			"total":          info.Run.Total,
			"created":        info.Run.Created,
			"moved":          info.Run.Moved,
			"updated":        info.Run.Updated,
			"no_match":       info.Run.NoMatch,
			"pinned_skipped": info.Run.PinnedSkipped,
		}
	}
	response.Success(c, payload)
}

func classificationPayload(applied bool, s service.PlanSummary, results []service.PeerClassificationResult) gin.H {
	return gin.H{
		"applied":        applied,
		"total":          s.Total,
		"created":        s.Created,
		"moved":          s.Moved,
		"updated":        s.Updated,
		"unchanged":      s.Unchanged,
		"no_match":       s.NoMatch,
		"pinned_skipped": s.PinnedSkipped,
		"results":        results,
	}
}
