package admin

// PeerClassificationRuleQuery is the list filter for classification rules.
// Scope is always forced to the current user in the controller.
type PeerClassificationRuleQuery struct {
	Active *int `form:"active"` // optional: 1/0 to filter by active state
	PageQuery
}

// PinForm sets or clears the pinned flag on a single address-book entry.
type PinForm struct {
	RowId  uint `json:"row_id" validate:"required,gt=0"`
	Pinned bool `json:"pinned"`
}
