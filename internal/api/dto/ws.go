package dto

// ClientMessage covers all messages a client may send over WS.
// In phase 0 the only payload is `subscribe`; other fields are ignored.
type ClientMessage struct {
	Type     string `json:"type"`
	SectorID int64  `json:"sectorID,omitempty"`
}
