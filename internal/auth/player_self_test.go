package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// stubSelfReader is a fixed PlayerSelf reader (cash + active ship) for the
// /api/player/me handler tests.
type stubSelfReader struct {
	cash         int64
	activeID     domain.ShipID
	activeSet    bool
	passengerID  domain.ShipID
	passengerSet bool
}

func (s stubSelfReader) GetCash(_ context.Context, _ domain.PlayerID) (int64, error) {
	return s.cash, nil
}

func (s stubSelfReader) ActiveShip(_ context.Context, _ domain.PlayerID) (domain.ShipID, bool, error) {
	return s.activeID, s.activeSet, nil
}

func (s stubSelfReader) PassengerHost(_ context.Context, _ domain.PlayerID) (domain.ShipID, bool, error) {
	return s.passengerID, s.passengerSet, nil
}

// playerSelfBody mirrors the relevant fields of PlayerSelfResponse for decoding.
type playerSelfBody struct {
	PlayerID          int64  `json:"playerID"`
	Login             string `json:"login"`
	Cash              int64  `json:"cash"`
	ActiveShipID      *int64 `json:"activeShipID"`
	PassengerOfShipID *int64 `json:"passengerOfShipID"`
}

func registerAndSelf(t *testing.T, reader stubSelfReader) playerSelfBody {
	t.Helper()
	srv, mux := newTestServer(t)
	srv.RegisterPlayerSelf(mux, reader)

	resp := doJSON(t, mux, http.MethodPost, "/api/auth/register",
		map[string]any{"login": "sofer", "password": "secret", "race": 1})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookie := sessionCookie(t, resp)

	me := doJSON(t, mux, http.MethodGet, "/api/player/me", nil, cookie)
	require.Equal(t, http.StatusOK, me.StatusCode)
	var body playerSelfBody
	require.NoError(t, json.NewDecoder(me.Body).Decode(&body))
	return body
}

func TestUnit_Handler_PlayerSelf_IncludesActiveShipID(t *testing.T) {
	body := registerAndSelf(t, stubSelfReader{cash: 12345, activeID: 55, activeSet: true})
	assert.EqualValues(t, 12345, body.Cash)
	require.NotNil(t, body.ActiveShipID, "active ship id must be present when set")
	assert.EqualValues(t, 55, *body.ActiveShipID)
}

func TestUnit_Handler_PlayerSelf_NullActiveShipWhenUnset(t *testing.T) {
	body := registerAndSelf(t, stubSelfReader{cash: 9000, activeSet: false})
	assert.EqualValues(t, 9000, body.Cash)
	assert.Nil(t, body.ActiveShipID, "active ship id must be null when unset")
}
