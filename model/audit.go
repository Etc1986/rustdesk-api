package model

const (
	AuditActionNew   = "new"
	AuditActionClose = "close"
)

type AuditConn struct {
	IdModel
	Action    string `json:"action" gorm:"default:'';not null;"`
	ConnId    int64  `json:"conn_id" gorm:"default:0;not null;index"`
	PeerId    string `json:"peer_id" gorm:"default:'';not null;index"`
	FromPeer  string `json:"from_peer" gorm:"default:'';not null;"`
	FromName  string `json:"from_name" gorm:"default:'';not null;"`
	Ip        string `json:"ip" gorm:"default:'';not null;"`
	SessionId string `json:"session_id" gorm:"default:'';not null;"`
	Type      int    `json:"type" gorm:"default:0;not null;"`
	Uuid      string `json:"uuid" gorm:"default:'';not null;"`
	CloseTime int64  `json:"close_time" gorm:"default:0;not null;"`
	TimeModel
}

type AuditConnList struct {
	AuditConns []*AuditConn `json:"list"`
	Pagination
}

type AuditFile struct {
	IdModel
	FromPeer string `json:"from_peer" gorm:"default:'';not null;index"`
	Info     string `json:"info" gorm:"default:'';not null;"`
	IsFile   bool   `json:"is_file" gorm:"default:0;not null;"`
	Path     string `json:"path" gorm:"default:'';not null;"`
	PeerId   string `json:"peer_id" gorm:"default:'';not null;index"`
	Type     int    `json:"type" gorm:"default:0;not null;"`
	Uuid     string `json:"uuid" gorm:"default:'';not null;"`
	Ip       string `json:"ip" gorm:"default:'';not null;"`
	Num      int    `json:"num" gorm:"default:0;not null;"`
	FromName string `json:"from_name" gorm:"default:'';not null;"`
	TimeModel
}

type AuditFileList struct {
	AuditFiles []*AuditFile `json:"list"`
	Pagination
}

// AuditAbBatch records one administrative "batch-add-to-address-book" operation.
// One row per invocation of BatchCreateFromPeers; counters summarise the outcome.
type AuditAbBatch struct {
	IdModel
	AdminId      uint `json:"admin_id" gorm:"default:0;not null;index"`
	UserId       uint `json:"user_id" gorm:"default:0;not null;index"`
	CollectionId uint `json:"collection_id" gorm:"default:0;not null"`
	Total        int  `json:"total" gorm:"default:0;not null"`
	Added        int  `json:"added" gorm:"default:0;not null"`
	Existing     int  `json:"existing" gorm:"default:0;not null"`
	NotFound     int  `json:"not_found" gorm:"default:0;not null"`
	Failed       int  `json:"failed" gorm:"default:0;not null"`
	TimeModel
}
