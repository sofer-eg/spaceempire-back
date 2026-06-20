package dto

// TransportRequest is the body of POST /api/cmd/transport-cargo (phase 10.3.18):
// teleport Quantity units of GoodsType from the player's SourceShipID into
// ShipID (the up_transporter ship), both in the same sector and within range.
type TransportRequest struct {
	ShipID       int64 `json:"shipID"`
	SourceShipID int64 `json:"sourceShipID"`
	GoodsType    int64 `json:"goodsType"`
	Quantity     int64 `json:"quantity"`
}

// TransportResponse is the ack of a successful cargo teleport.
type TransportResponse struct {
	OK bool `json:"ok"`
}
