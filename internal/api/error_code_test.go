package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// TASK-185. Two things are pinned here.
//
//  1. The overloaded statuses carry a machine-readable `code`. The SPA used to
//     tell «ship is docked» from «jump blocked by antijump field» — both 409 —
//     by matching the English sentinel text (front/src/api.ts jumpDriveErrorText,
//     installErrorText). Russianising the sentinels would have broken that
//     silently, so the pairs now travel as a code the wording cannot affect.
//  2. Every player-facing message in the eighteen space-command handlers is
//     Russian. That is checked over the source (see the AST test at the bottom)
//     rather than one response at a time: 216 write sites cannot each be reached
//     through HTTP, and the acceptance criterion is about all of them.

// cannedRouter answers one queued command with a canned sentinel, so the
// handler's error switch can be walked branch by branch without building a
// worker that can genuinely be jammed, docked or shield-damaged. Send returns
// sendErr when set (that is how ErrInboxFull reaches the handler).
type cannedRouter struct {
	replyErr error
	sendErr  error
}

func (r cannedRouter) Send(_ domain.SectorID, cmd sector.Command) error {
	if r.sendErr != nil {
		return r.sendErr
	}
	switch c := cmd.(type) {
	case sector.JumpDriveCommand:
		c.Reply <- sector.CmdResult{Err: r.replyErr}
	case sector.InstallSatelliteCommand:
		c.Reply <- sector.InstallSatelliteResult{Err: r.replyErr}
	case sector.InstallJammerCommand:
		c.Reply <- sector.InstallJammerResult{Err: r.replyErr}
	}
	return nil
}

func (r cannedRouter) Snapshot(domain.SectorID) sector.Snapshot { return sector.Snapshot{} }

func (r cannedRouter) Subscribe(context.Context, domain.SectorID, domain.PlayerID) (*sector.Subscription, func(), error) {
	return nil, func() {}, nil
}

// The handlers look the ship up before sending; sector 1 keeps them on the
// happy path so the canned reply is what decides the response.
func (r cannedRouter) LookupShipSector(domain.ShipID) (domain.SectorID, bool) {
	return domain.SectorID(1), true
}

func (r cannedRouter) LookupPrimaryShipByPlayer(domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return domain.ShipID(1), domain.SectorID(1), true
}

func cannedServer(r cannedRouter) *api.Server {
	return api.NewServer(r, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
}

// errorBody is the JSON every failing command answers with. Code is absent on
// the branches the SPA can already tell apart by status alone.
type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func postCommand(t *testing.T, srv *api.Server, path string, body any) (int, errorBody) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var parsed errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed), rec.Body.String())
	return rec.Code, parsed
}

// hasCyrillic reports whether s contains at least one Cyrillic letter. Messages
// may still carry Latin identifiers the player is meant to see (up_exdocking,
// up_jump_drive), so the test is "there is Russian in it", not "there is no
// Latin in it".
func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// TestUnit_JumpDriveErrorCodes walks every jump-drive branch the SPA has to
// disambiguate. 409, 422, 400 and 503 each cover two outcomes with different
// advice attached, and jumpDriveErrorText picks between them on the code.
func TestUnit_JumpDriveErrorCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		reply    error
		send     error
		status   int
		code     string
		wantCode bool
	}{
		{name: "docked", reply: sector.ErrShipDocked, status: http.StatusConflict, code: api.CodeShipDocked, wantCode: true},
		{name: "antijump", reply: sector.ErrJumpBlockedByAntijump, status: http.StatusConflict, code: api.CodeJumpBlockedAntijump, wantCode: true},
		{name: "no drive", reply: sector.ErrEquipmentRequired, status: http.StatusUnprocessableEntity, code: api.CodeJumpDriveRequired, wantCode: true},
		{name: "shield", reply: sector.ErrShieldRequired, status: http.StatusUnprocessableEntity, code: api.CodeShieldRequired, wantCode: true},
		{name: "forbidden sector", reply: sector.ErrJumpForbiddenSector, status: http.StatusBadRequest, code: api.CodeJumpForbiddenSector, wantCode: true},
		{name: "invalid sector", reply: sector.ErrInvalidSector, status: http.StatusBadRequest, wantCode: false},
		{name: "inbox full", send: sector.ErrInboxFull, status: http.StatusServiceUnavailable, code: api.CodeSectorBusy, wantCode: true},
		{name: "handoff unavailable", reply: sector.ErrHandoffUnavailable, status: http.StatusServiceUnavailable, wantCode: false},
		{name: "not found", reply: sector.ErrShipNotFound, status: http.StatusNotFound, wantCode: false},
		{name: "forbidden", reply: sector.ErrForbidden, status: http.StatusForbidden, wantCode: false},
		{name: "cooldown", reply: sector.ErrJumpOnCooldown, status: http.StatusTooManyRequests, wantCode: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := cannedServer(cannedRouter{replyErr: tc.reply, sendErr: tc.send})

			status, body := postCommand(t, srv, "/api/cmd/jump-drive", map[string]int64{"shipID": 1, "targetSectorID": 2})

			require.Equal(t, tc.status, status)
			assert.True(t, hasCyrillic(body.Error), "message must be Russian, got %q", body.Error)
			if tc.wantCode {
				assert.Equal(t, tc.code, body.Code)
			} else {
				assert.Empty(t, body.Code, "no code on a status the SPA can read alone")
			}
		})
	}
}

// TestUnit_InstallErrorCodes: the two install commands overload 400 (docked /
// empty hold / malformed request) and 503 (busy sector / no installer wired),
// and installErrorText words each of those differently. Both handlers must code
// the pairs identically — the SPA has one mapper for the two.
func TestUnit_InstallErrorCodes(t *testing.T) {
	t.Parallel()

	paths := []string{"/api/cmd/install-satellite", "/api/cmd/install-jammer"}
	cases := []struct {
		name     string
		reply    error
		send     error
		status   int
		code     string
		wantCode bool
	}{
		{name: "docked", reply: sector.ErrShipDocked, status: http.StatusBadRequest, code: api.CodeShipDocked, wantCode: true},
		{name: "empty hold", reply: cargo.ErrInsufficientQuantity, status: http.StatusBadRequest, code: api.CodeCargoInsufficient, wantCode: true},
		{name: "inbox full", send: sector.ErrInboxFull, status: http.StatusServiceUnavailable, code: api.CodeSectorBusy, wantCode: true},
		{name: "no installer", reply: sector.ErrInstallerUnavailable, status: http.StatusServiceUnavailable, wantCode: false},
		{name: "not found", reply: sector.ErrShipNotFound, status: http.StatusNotFound, wantCode: false},
		{name: "forbidden", reply: sector.ErrForbidden, status: http.StatusForbidden, wantCode: false},
	}

	for _, path := range paths {
		for _, tc := range cases {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				t.Parallel()
				srv := cannedServer(cannedRouter{replyErr: tc.reply, sendErr: tc.send})

				status, body := postCommand(t, srv, path, map[string]int64{"shipID": 1})

				require.Equal(t, tc.status, status)
				assert.True(t, hasCyrillic(body.Error), "message must be Russian, got %q", body.Error)
				if tc.wantCode {
					assert.Equal(t, tc.code, body.Code)
				} else {
					assert.Empty(t, body.Code)
				}
			})
		}
	}
}

// spaceCommandFiles is the TASK-185 scope, listed once so the boundary is data
// rather than a claim in a commit message: the handlers behind the four space
// screens (ObjectActionsMenu, CombatHUD, TargetsPanel, SpacePointMenu). Trade,
// auction, cargo, auth and the rest are a separate task (TASK-198) and are
// deliberately absent.
var spaceCommandFiles = []string{
	"attack.go", "capture.go", "dismantle_static.go", "dock.go", "external_dock.go",
	"hack.go", "install_jammer.go", "install_satellite.go", "jump.go", "jump_drive.go",
	"launch_drone.go", "launch_missile.go", "launch_torpedo.go", "mine.go", "move.go",
	"pickup_container.go", "recall_drones.go", "set_course.go",
}

// TestUnit_SpaceCommandMessagesAreRussian reads the handlers themselves. The
// player-facing half of these files is 216 write sites; only a handful are
// reachable from a unit test, and «русифицированы все 18 файлов целиком» is a
// statement about the source, so the source is what is checked.
//
// Only string literals are inspected: writeError(w, status, err.Error()) hands
// through a server-side error the SPA hides behind its own 5xx wording, and
// there is nothing to translate in it.
func TestUnit_SpaceCommandMessagesAreRussian(t *testing.T) {
	t.Parallel()

	writers := map[string]bool{"writeError": true, "writeErrorCode": true, "writeIfTransient": true}
	total := 0

	for _, name := range spaceCommandFiles {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, name)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || !writers[fn.Name] {
				return true
			}
			last, ok := call.Args[len(call.Args)-1].(*ast.BasicLit)
			if !ok || last.Kind != token.STRING {
				return true
			}
			msg, err := strconv.Unquote(last.Value)
			require.NoError(t, err)
			total++
			assert.True(t, hasCyrillic(msg),
				"%s:%d %s: player-facing message must be Russian, got %q",
				name, fset.Position(last.Pos()).Line, fn.Name, msg)
			return true
		})
	}

	// The count is the scope: 216 write sites, of which the ones passing
	// err.Error() through carry no literal. A drop here means a handler lost its
	// message, a jump means one arrived untranslated in a file nobody re-checked.
	assert.Equal(t, 220, total, "player-facing literals across the space commands")
}
