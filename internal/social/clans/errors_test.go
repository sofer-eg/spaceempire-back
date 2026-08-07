package clans

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
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
// This walks the sentinels through the real handler mapper rather than
// asserting the constants, because the prefix could just as well come back via
// a wrapper: it is what reaches the response body that matters.
//
// Which sentinels get walked is not a hand-written list. A Go test cannot get a
// package var by name, so the values still have to be spelled out — but the
// *names* are read out of errors.go by the AST (declaredSentinels below), the
// same way internal/api enumerates its handlers, and a sentinel declared there
// and missing here fails immediately. The earlier require.Len(sentinels, 14)
// could not do that: a fifteenth sentinel that nobody added to the list left the
// length at 14 and the test green.
func TestUnit_ClanErrorsReachThePlayerInRussian(t *testing.T) {
	t.Parallel()

	walked := map[string]error{
		"ErrNameTaken":          ErrNameTaken,
		"ErrTagTaken":           ErrTagTaken,
		"ErrAlreadyInClan":      ErrAlreadyInClan,
		"ErrClanNotFound":       ErrClanNotFound,
		"ErrNotMember":          ErrNotMember,
		"ErrForbidden":          ErrForbidden,
		"ErrTargetNotMember":    ErrTargetNotMember,
		"ErrCannotKickLeader":   ErrCannotKickLeader,
		"ErrInvalidRole":        ErrInvalidRole,
		"ErrCannotChangeLeader": ErrCannotChangeLeader,
		"ErrLeaderMustTransfer": ErrLeaderMustTransfer,
		"ErrInvitationNotFound": ErrInvitationNotFound,
		"ErrAlreadyInvited":     ErrAlreadyInvited,
		"ErrInvalidInput":       ErrInvalidInput,
	}

	declared := declaredSentinels(t)
	assert.Len(t, walked, len(declared),
		"one entry per sentinel in errors.go — add the new one, or drop the removed one")

	srv := NewServer(nil, slog.New(slog.DiscardHandler))
	for _, name := range declared {
		sentinel, ok := walked[name]
		require.True(t, ok, "%s is declared in errors.go but never walked here", name)

		rec := httptest.NewRecorder()
		srv.writeServiceError(rec, sentinel)

		var body struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())

		assert.NotEqual(t, http.StatusInternalServerError, rec.Code,
			"%s is a player-facing refusal, not a server fault", name)
		assert.NotContains(t, body.Error, "clans:", "the domain prefix must not reach the player")
		assert.True(t, hasCyrillic(body.Error), "%s must be Russian, got %q", name, body.Error)
		assert.False(t, hasLatin(body.Error), "no English left in %s: %q", name, body.Error)
	}
}

// declaredSentinels lists the package-level vars errors.go declares — the file
// holds nothing else, so every name it returns is a sentinel a player can be
// shown.
func declaredSentinels(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	require.NoError(t, err)

	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			names = append(names, identNames(spec.(*ast.ValueSpec).Names)...)
		}
	}
	require.NotEmpty(t, names, "errors.go declares no sentinels — did the file move?")
	return names
}

func identNames(idents []*ast.Ident) []string {
	names := make([]string, 0, len(idents))
	for _, ident := range idents {
		names = append(names, ident.Name)
	}
	return names
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
