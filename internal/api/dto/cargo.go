package dto

import "spaceempire/back/internal/domain"

// CargoItem mirrors domain.CargoItem on the wire.
type CargoItem struct {
	TypeID   int32 `json:"typeID"`
	Quantity int64 `json:"quantity"`
}

// CargoInventoryResponse is the body of GET /api/ship/{id}/cargo and
// GET /api/station/{id}/cargo. Items may be empty.
type CargoInventoryResponse struct {
	OwnerKind int         `json:"ownerKind"`
	OwnerID   int64       `json:"ownerID"`
	Capacity  float64     `json:"capacity"`
	Used      float64     `json:"used"`
	Items     []CargoItem `json:"items"`
}

// MoveCargoRequest is the body of POST /api/cmd/cargo/move.
type MoveCargoRequest struct {
	From     EntityRef `json:"from"`
	To       EntityRef `json:"to"`
	TypeID   int32     `json:"typeID"`
	Quantity int64     `json:"quantity"`
}

type MoveCargoResponse struct {
	OK bool `json:"ok"`
}

// CargoItemsFromDomain converts a slice of domain CargoItems into the
// wire format. nil input yields an empty (non-nil) slice for stable JSON.
func CargoItemsFromDomain(in []domain.CargoItem) []CargoItem {
	out := make([]CargoItem, 0, len(in))
	for _, it := range in {
		out = append(out, CargoItem{TypeID: int32(it.GoodsType), Quantity: it.Quantity})
	}
	return out
}

// CargoInventoryFromDomain builds the response body from a domain
// inventory snapshot.
func CargoInventoryFromDomain(inv domain.Inventory) CargoInventoryResponse {
	return CargoInventoryResponse{
		OwnerKind: int(inv.Owner.Kind),
		OwnerID:   inv.Owner.ID,
		Capacity:  inv.Capacity,
		Used:      inv.Used,
		Items:     CargoItemsFromDomain(inv.Items),
	}
}
