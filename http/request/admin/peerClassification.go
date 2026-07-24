package admin

// PeerClassificationRuleQuery is the list filter for classification rules.
// Scope is always forced to the current user in the controller.
type PeerClassificationRuleQuery struct {
	Active *int `form:"active"` // optional: 1/0 to filter by active state
	PageQuery
}
