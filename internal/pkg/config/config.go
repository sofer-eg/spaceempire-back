package config

import (
	"os"
	"time"

	"github.com/cristalhq/aconfig"
	"github.com/cristalhq/aconfig/aconfigyaml"
)

type Config struct {
	Server        ServerConfig
	Sector        SectorConfig
	Postgres      PostgresConfig
	Auth          AuthConfig
	Balance       BalanceConfig
	Observability ObservabilityConfig
}

// ObservabilityConfig configures logging, /metrics and /debug/* (phase 7.1).
type ObservabilityConfig struct {
	// LogFormat is "text" (dev, default) or "json" (production).
	LogFormat string `env:"SE_LOG_FORMAT" default:"text"`
	// LogLevel is debug|info|warn|error.
	LogLevel string `env:"SE_LOG_LEVEL" default:"info"`
	// LogFile, when set, writes rotated JSON logs there (lumberjack). Empty =
	// stdout only. When set, LogFormat is forced to json.
	LogFile       string `env:"SE_LOG_FILE" default:""`
	LogMaxSizeMB  int    `default:"100"`
	LogMaxBackups int    `default:"7"`
	LogMaxAgeDays int    `default:"14"`
	// DebugUser/DebugPass protect /metrics and /debug/* with HTTP basic auth.
	// An empty DebugUser leaves them open (dev); set both in production.
	DebugUser string `env:"SE_DEBUG_USER" default:""`
	DebugPass string `env:"SE_DEBUG_PASS" default:""`
}

type ServerConfig struct {
	Port            int           `default:"8080"`
	ShutdownTimeout time.Duration `default:"10s"`
}

type SectorConfig struct {
	TickInterval     time.Duration `default:"3s"`
	SnapshotInterval time.Duration `default:"5s"`
	InboxCapacity    int           `default:"256"`
	// WorkersCount is the number of sector worker goroutines in the pool.
	// At 1 (the default), a single worker owns every sector. Larger values
	// round-robin sectors across workers; raise this only when the metric
	// tick_overrun_total starts climbing.
	WorkersCount int `default:"1"`
	// BoundsRadius is the rendering half-extent of a sector in world units.
	// Engine physics is unbounded; this is a render-only constraint shipped
	// to the SPA via the WS welcome so the canvas knows the sector edge.
	// Init-time validator just WARN-logs entities outside the box — no
	// clamp, no reject.
	BoundsRadius float64 `default:"5000"`
	// NearZoomRadius is the half-side of the Near map-zoom window the SPA
	// builds around the player's own ship. Surfaced via WS welcome so the
	// client doesn't hard-code it.
	NearZoomRadius float64 `default:"125"`
	// DockRange is the world-unit radius inside which DockCommand is
	// accepted. Surfaced via WS welcome so the SPA can enable the dock
	// affordance based on local distance to the static. Mirrors
	// sector.Config.DockRange (default 3).
	DockRange float64 `default:"3"`
	// GateRange is the world-unit radius inside which JumpCommand is
	// accepted. Surfaced via WS welcome so the SPA can enable the jump
	// affordance for gate targets. Mirrors sector.Config.GateRange
	// (default 50).
	GateRange float64 `default:"50"`
}

type PostgresConfig struct {
	DSN         string        `env:"PG_DSN" default:"postgres://enlarge_db:enlarge2501@localhost:5432/spaceempire?sslmode=disable"`
	MaxConns    int32         `default:"10"`
	ConnTimeout time.Duration `default:"5s"`
	// AutoMigrate runs goose.Up at startup. Off in tests that use testdb.Setup.
	AutoMigrate bool `default:"true"`
}

type BalanceConfig struct {
	// Path to the balance YAML loaded once at startup. Defaults to the
	// in-tree configs/balance.yaml so `make run` works out of the box.
	Path string `default:"configs/balance.yaml"`
	// ShipClassesPath is the ship-class catalog YAML (ct_ship_classes),
	// converted by cmd/starwind-tools/convert-ship-classes. Loaded at
	// startup and exposed via GET /api/ship-classes.
	ShipClassesPath string `default:"configs/ship_classes.yaml"`
	// StationTypesPath is the station-type catalog + production recipes YAML
	// (station_types + station_goods_types), converted by
	// cmd/starwind-tools/convert-station-types. Loaded at startup: the catalog
	// is exposed via GET /api/station-types, the recipes feed production.
	StationTypesPath string `default:"configs/station_types.yaml"`
	// EquipmentPath is the ship-equipment catalog YAML (ct_updates), converted
	// by cmd/starwind-tools/convert-equipment. Loaded at startup and exposed
	// via GET /api/equipment.
	EquipmentPath string `default:"configs/equipment.yaml"`
}

type AuthConfig struct {
	// SessionTTL is the lifetime of a freshly issued session cookie. The
	// browser cookie's MaxAge mirrors this, and Service uses it to set the
	// row's expires_at.
	SessionTTL time.Duration `default:"168h"` // 7 days
	// CookieSecure marks the session cookie as Secure (HTTPS-only). Leave
	// off in dev so the cookie survives http://localhost.
	CookieSecure bool `default:"false"`
	// BcryptCost is the bcrypt work factor. 0 → bcrypt.DefaultCost (10).
	BcryptCost int `default:"10"`
}

func Load() (*Config, error) {
	cfg := &Config{}

	acfg := aconfig.Config{
		EnvPrefix:          "SE",
		AllowUnknownFields: true,
		SkipFlags:          true,
		FileDecoders: map[string]aconfig.FileDecoder{
			".yaml": aconfigyaml.New(),
			".yml":  aconfigyaml.New(),
		},
	}

	if path := os.Getenv("CONFIG_PATH"); path != "" {
		acfg.Files = []string{path}
	} else {
		acfg.SkipFiles = true
	}

	if err := aconfig.LoaderFor(cfg, acfg).Load(); err != nil {
		return nil, err
	}

	return cfg, nil
}
