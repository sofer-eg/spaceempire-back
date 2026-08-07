package clans

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-185. writeServiceError hands the sentinel's own text to the player
// verbatim, so a clan refusal reads exactly as errors.go words it. Two things
// had to be true and were not: the wording was English («clans: name already
// taken» on the Russian Кланы tab), and it carried the package name — an
// internal detail the player has no use for.
//
// This walks all fourteen sentinels through the real handler mapper rather than
// asserting the constants, because the prefix could just as well come back via
// a wrapper: it is what reaches the response body that matters.
func TestUnit_ClanErrorsReachThePlayerInRussian(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		ErrNameTaken, ErrTagTaken, ErrAlreadyInClan, ErrClanNotFound, ErrNotMember,
		ErrForbidden, ErrTargetNotMember, ErrCannotKickLeader, ErrInvalidRole,
		ErrCannotChangeLeader, ErrLeaderMustTransfer, ErrInvitationNotFound,
		ErrAlreadyInvited, ErrInvalidInput,
	}
	require.Len(t, sentinels, 14, "every sentinel in errors.go is covered")

	srv := NewServer(nil, slog.New(slog.DiscardHandler))
	for _, sentinel := range sentinels {
		rec := httptest.NewRecorder()
		srv.writeServiceError(rec, sentinel)

		var body struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())

		assert.NotEqual(t, http.StatusInternalServerError, rec.Code,
			"%v is a player-facing refusal, not a server fault", sentinel)
		assert.NotContains(t, body.Error, "clans:", "the domain prefix must not reach the player")
		assert.True(t, hasCyrillic(body.Error), "message must be Russian, got %q", body.Error)
		assert.False(t, hasLatin(body.Error), "no English left in %q", body.Error)
	}
}

// The two validation refusals are the only clan errors that reach the handler
// wrapped (fmt.Errorf("%w: …", ErrInvalidInput)), and err.Error() prints the
// wrapper's tail after the sentinel. That tail used to be «name length must be
// 3..32» — Russian sentinel, English explanation.
func TestUnit_ClanValidationTailIsRussian(t *testing.T) {
	t.Parallel()

	svc := NewService(nil) // validation refuses before the repo is touched
	for _, tc := range []struct{ name, tag string }{
		{name: "ab", tag: "TAG"},
		{name: "нормальное имя", tag: "T"},
	} {
		_, err := svc.Create(context.Background(), 1, tc.name, tc.tag)
		require.ErrorIs(t, err, ErrInvalidInput)

		rec := httptest.NewRecorder()
		NewServer(nil, slog.New(slog.DiscardHandler)).writeServiceError(rec, err)

		var body struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.False(t, hasLatin(body.Error), "wrapper tail must be Russian too, got %q", body.Error)
	}
}

func hasCyrillic(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) })
}

func hasLatin(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Latin, r) })
}
