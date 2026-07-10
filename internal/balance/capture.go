package balance

// CaptureConfig is the capture/hack/knockoff tuning loaded from
// configs/capture.yaml (ЧТЗ doc-4 §5.1). TASK-100.3.9.1 owns the four Knock*
// fields (SP DestroyModule); the capture (.4) and hack (.3) sub-tasks extend
// this struct with their own thresholds. Kept in the balance package alongside
// the other catalogs; the app maps the knock subset into combat.KnockConfig for
// the sector worker.
type CaptureConfig struct {
	KnockCriticalShieldCharge  float64
	KnockCriticalHullIntegrity float64
	KnockExternalBase          float64
	KnockInternalBase          float64
}

// DefaultCaptureConfig returns the faithful DestroyModule scalars (ЧТЗ §5.1).
// Used as the baseline the loader fills missing/zero fields from.
func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		KnockCriticalShieldCharge:  0.2,
		KnockCriticalHullIntegrity: 0.7,
		KnockExternalBase:          0.2,
		KnockInternalBase:          0.1,
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
	return c
}
