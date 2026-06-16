package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// GoodsCatalog is the slice of *balance.Balance the goods endpoint needs.
// Declared per ISP so handler tests can stub it without spinning up a real
// catalog file. Get backs the trade_up scanner's price-tier classification
// (phase 10.3.12) — it reads each good's [avg, max] band.
type GoodsCatalog interface {
	AllGoods() []balance.Goods
	Get(id domain.GoodsTypeID) (balance.Goods, bool)
}

// handleGoods returns the static goods catalog (id, name, space) used by
// the SPA to label market rows, cargo lists and auction lots. Read-only,
// open (no auth) — the catalog ships with the client anyway.
func (s *Server) handleGoods(w http.ResponseWriter, _ *http.Request) {
	if s.goods == nil {
		writeError(w, http.StatusServiceUnavailable, "goods catalog not available")
		return
	}
	all := s.goods.AllGoods()
	items := make([]dto.Goods, 0, len(all))
	for _, g := range all {
		items = append(items, dto.Goods{
			TypeID: int32(g.ID),
			Name:   g.Name,
			Space:  int32(g.Space),
		})
	}
	writeJSON(w, http.StatusOK, dto.GoodsListResponse{Items: items})
}
