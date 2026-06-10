package dto

import (
	"time"

	auctionrepo "spaceempire/back/internal/persistence/auction"
)

// AuctionLot is the wire shape of one lot returned by GET /api/auction
// and POST /api/auction.
type AuctionLot struct {
	ID              int64     `json:"id"`
	SellerID        int64     `json:"sellerID"`
	GoodsTypeID     int32     `json:"goodsTypeID"`
	Quantity        int64     `json:"quantity"`
	Source          EntityRef `json:"source"`
	StartPrice      int64     `json:"startPrice"`
	CurrentPrice    int64     `json:"currentPrice"`
	CurrentBidderID *int64    `json:"currentBidderID,omitempty"`
	EndsAt          time.Time `json:"endsAt"`
	Status          int16     `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
}

// AuctionLotFromRepo packages one repo row for the wire.
func AuctionLotFromRepo(l auctionrepo.Lot) AuctionLot {
	out := AuctionLot{
		ID:           l.ID,
		SellerID:     int64(l.SellerID),
		GoodsTypeID:  int32(l.GoodsType),
		Quantity:     l.Quantity,
		Source:       EntityRef{Kind: int(l.Source.Kind), ID: l.Source.ID},
		StartPrice:   l.StartPrice,
		CurrentPrice: l.CurrentPrice,
		EndsAt:       l.EndsAt,
		Status:       int16(l.Status),
		CreatedAt:    l.CreatedAt,
	}
	if l.CurrentBidderID != nil {
		b := int64(*l.CurrentBidderID)
		out.CurrentBidderID = &b
	}
	return out
}

// AuctionListResponse is the body of GET /api/auction.
type AuctionListResponse struct {
	Lots []AuctionLot `json:"lots"`
}

// AuctionCreateRequest is the body of POST /api/auction.
// DurationSeconds bounds: 60..604800 (1 minute .. 7 days), enforced by Service.
type AuctionCreateRequest struct {
	Source          EntityRef `json:"source"`
	GoodsTypeID     int32     `json:"goodsTypeID"`
	Quantity        int64     `json:"quantity"`
	StartPrice      int64     `json:"startPrice"`
	DurationSeconds int64     `json:"durationSeconds"`
}

// AuctionBidRequest is the body of POST /api/auction/{id}/bid. ShipID is
// the bidder's docked ship — bidding (like all commerce) requires a dock.
type AuctionBidRequest struct {
	ShipID int64 `json:"shipID"`
	Amount int64 `json:"amount"`
}

// AuctionBidResponse matches the API contract from the task spec.
type AuctionBidResponse struct {
	NewPrice  int64 `json:"newPrice"`
	NewLeader bool  `json:"newLeader"`
}
