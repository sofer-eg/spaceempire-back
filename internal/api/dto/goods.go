package dto

// Goods is one row of the static goods catalog returned by GET /api/goods.
// Mirrors balance.Goods but exposes only the fields the SPA renders.
type Goods struct {
	TypeID int32  `json:"typeID"`
	Name   string `json:"name"`
	Space  int32  `json:"space"`
}

// GoodsListResponse is the body of GET /api/goods.
type GoodsListResponse struct {
	Items []Goods `json:"items"`
}
