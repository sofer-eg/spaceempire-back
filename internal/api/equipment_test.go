package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
)

// TestUnit_HandleEquipment_ExposesReputationGates verifies GET /api/equipment
// carries the reputation thresholds (minWarRate / minTradeRate / minRaceRate)
// from balance.Equipment, so the outfitting screen can pre-block an install by
// reputation instead of learning about it only from a 422 (TASK-100.3.27).
// fakeEquipCatalog is defined in launch_torpedo_test.go (same package).
func TestUnit_HandleEquipment_ExposesReputationGates(t *testing.T) {
	t.Parallel()

	equip := fakeEquipCatalog{items: []balance.Equipment{{
		ID:          1,
		Type:        "up_launcher",
		Description: "launcher",
		MinWarRate:  2,
		MinRaceRate: 9,
	}}}
	srv := api.NewServer(workerRouter{}, api.Config{Equipment: equip}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/equipment", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The keys must always be present (no omitempty) — the front compares them
	// as numbers, so a missing key would read as undefined.
	body := rec.Body.String()
	require.Contains(t, body, "minWarRate")
	require.Contains(t, body, "minTradeRate")
	require.Contains(t, body, "minRaceRate")

	var resp dto.EquipmentListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Items, 1)
	require.Equal(t, 2, resp.Items[0].MinWarRate)
	require.Equal(t, 0, resp.Items[0].MinTradeRate)
	require.Equal(t, 9, resp.Items[0].MinRaceRate)
}
