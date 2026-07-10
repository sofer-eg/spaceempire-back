package balance

// CaptureConfig is the capture/hack/knockoff tuning loaded from
// configs/capture.yaml (ЧТЗ doc-4 §5.1). TASK-100.3.9.1 owns the four Knock*
// fields (SP DestroyModule); the hack sub-task (.3) owns the five Hack* fields
// (SP UseHack); the capture sub-task (.4) owns the three Capture* fields (SP
// DoCapture). Kept in the balance package alongside the other catalogs; the app
// maps the knock subset into combat.KnockConfig, the hack subset into the sector
// worker's HackRange + the app-side station robber, and the capture subset into
// the sector worker's Capture* config.
type CaptureConfig struct {
	KnockCriticalShieldCharge  float64
	KnockCriticalHullIntegrity float64
	KnockExternalBase          float64
	KnockInternalBase          float64

	// HackRange is the max distance (world units) between the hacker ship and the
	// trade station (SP UseHack, TASK-100.3.9.3).
	HackRange float64
	// HackGoodsMinFraction is the minimum stock (fraction of the good's max_stock)
	// the richest good must hold for the station to be hackable ("too little goods
	// to fool the system").
	HackGoodsMinFraction float64
	// HackRobFraction is the fraction of the target good's stock stolen into loot.
	HackRobFraction float64
	// HackDamageFraction is the fraction of the target good's stock destroyed.
	HackDamageFraction float64
	// HackReputationPenalty is the k scalar of the standing penalty:
	// round((robbed+damaged)/max_stock * k) is subtracted from the hacker's
	// standing with the station's race (FR-D5, NFR-004).
	HackReputationPenalty float64

	// CaptureChance / KhaakCaptureChance are the ship-capture roll thresholds on a
	// 0..1000 scale (SP DoCapture, TASK-100.3.9.4): a capture succeeds when
	// rng.Float64()*1000 > threshold. CaptureChance (819, ~18%) is generic;
	// KhaakCaptureChance (876, ~12%) applies to a Kha'ak target.
	CaptureChance      float64
	KhaakCaptureChance float64
	// CaptureRange is the max distance (world units) between the attacker and the
	// target ship for a capture (SP DoCapture, √2500 = 50).
	CaptureRange float64
}

// DefaultCaptureConfig returns the faithful DestroyModule scalars (ЧТЗ §5.1).
// Used as the baseline the loader fills missing/zero fields from.
func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		KnockCriticalShieldCharge:  0.2,
		KnockCriticalHullIntegrity: 0.7,
		KnockExternalBase:          0.2,
		KnockInternalBase:          0.1,
		HackRange:                  50,
		HackGoodsMinFraction:       0.3,
		HackRobFraction:            0.15,
		HackDamageFraction:         0.05,
		HackReputationPenalty:      50,
		CaptureChance:              819,
		KhaakCaptureChance:         876,
		CaptureRange:               50,
	}
}

// withDefaults fills any non-positive field from DefaultCaptureConfig so a
// partial or empty capture.yaml still yields a usable config.
func (c CaptureConfig) withDefaults() CaptureConfig {
	d := DefaultCaptureConfig()
	if c.KnockCriticalShieldCharge <= 0 {
		c.KnockCriticalShieldCharge = d.KnockCriticalShieldCharge
	}
	if c.KnockCriticalHullIntegrity <= 0 {
		c.KnockCriticalHullIntegrity = d.KnockCriticalHullIntegrity
	}
	if c.KnockExternalBase <= 0 {
		c.KnockExternalBase = d.KnockExternalBase
	}
	if c.KnockInternalBase <= 0 {
		c.KnockInternalBase = d.KnockInternalBase
	}
	if c.HackRange <= 0 {
		c.HackRange = d.HackRange
	}
	if c.HackGoodsMinFraction <= 0 {
		c.HackGoodsMinFraction = d.HackGoodsMinFraction
	}
	if c.HackRobFraction <= 0 {
		c.HackRobFraction = d.HackRobFraction
	}
	if c.HackDamageFraction <= 0 {
		c.HackDamageFraction = d.HackDamageFraction
	}
	if c.HackReputationPenalty <= 0 {
		c.HackReputationPenalty = d.HackReputationPenalty
	}
	if c.CaptureChance <= 0 {
		c.CaptureChance = d.CaptureChance
	}
	if c.KhaakCaptureChance <= 0 {
		c.KhaakCaptureChance = d.KhaakCaptureChance
	}
	if c.CaptureRange <= 0 {
		c.CaptureRange = d.CaptureRange
	}
	return c
}
