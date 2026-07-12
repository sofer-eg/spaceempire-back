package racestanding_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/social/racestanding"
)

// stubReputation is a fixed war/trade reader for server tests.
type stubReputation struct {
	rep players.Reputation
	err error
}

func (s stubReputation) GetReputation(_ context.Context, _ domain.PlayerID) (players.Reputation, error) {
	return s.rep, s.err
}

// getStandings issues GET /api/my/race-standings for pid against srv and
// returns the recorder plus the decoded top-level JSON object (keys preserved
// so a test can assert a field is present, not merely zero).
func getStandings(t *testing.T, srv *racestanding.Server, pid domain.PlayerID) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux, func(h http.Handler) http.Handler { return h })
	req := httptest.NewRequest(http.MethodGet, "/api/my/race-standings", nil)
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), pid))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, body
}

func TestUnit_RaceStandingServer_ReturnsWarTradeRates(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(
		racestanding.Row{Player: player, Race: argon, Standing: -4},
		racestanding.Row{Player: player, Race: boron, Standing: 3},
	), racestanding.Config{WantedThreshold: -10})
	srv := racestanding.NewServer(svc, stubReputation{rep: players.Reputation{War: 2, Trade: 3}}, nil)

	rec, body := getStandings(t, srv, player)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, body, "warRate")
	require.Contains(t, body, "tradeRate")

	var warRate, tradeRate, wantedThreshold int
	require.NoError(t, json.Unmarshal(body["warRate"], &warRate))
	require.NoError(t, json.Unmarshal(body["tradeRate"], &tradeRate))
	require.NoError(t, json.Unmarshal(body["wantedThreshold"], &wantedThreshold))
	assert.Equal(t, 2, warRate)
	assert.Equal(t, 3, tradeRate)
	assert.Equal(t, -10, wantedThreshold, "existing wantedThreshold field unaffected")

	// items are still the full per-race list (5 reported races).
	var items []struct {
		Race     int  `json:"race"`
		Standing int  `json:"standing"`
		Wanted   bool `json:"wanted"`
	}
	require.NoError(t, json.Unmarshal(body["items"], &items))
	assert.Len(t, items, 5, "one row per reported race still returned")
}

func TestUnit_RaceStandingServer_ReputationNotFoundDefaultsToZero(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), racestanding.Config{})
	srv := racestanding.NewServer(svc, stubReputation{err: players.ErrPlayerNotFound}, nil)

	rec, body := getStandings(t, srv, player)

	assert.Equal(t, http.StatusOK, rec.Code, "missing reputation row must not 500")
	require.Contains(t, body, "warRate")
	require.Contains(t, body, "tradeRate")

	var warRate, tradeRate int
	require.NoError(t, json.Unmarshal(body["warRate"], &warRate))
	require.NoError(t, json.Unmarshal(body["tradeRate"], &tradeRate))
	assert.Zero(t, warRate)
	assert.Zero(t, tradeRate)
}

func TestUnit_RaceStandingServer_NilReaderDefaultsToZero(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), racestanding.Config{})
	srv := racestanding.NewServer(svc, nil, nil)

	rec, body := getStandings(t, srv, player)

	assert.Equal(t, http.StatusOK, rec.Code)
	var warRate, tradeRate int
	require.NoError(t, json.Unmarshal(body["warRate"], &warRate))
	require.NoError(t, json.Unmarshal(body["tradeRate"], &tradeRate))
	assert.Zero(t, warRate)
	assert.Zero(t, tradeRate)
}
