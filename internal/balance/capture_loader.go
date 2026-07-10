package balance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// yamlCaptureFile mirrors the on-disk shape of configs/capture.yaml. Only the
// knockoff fields owned by TASK-100.3.9.1 are read; unknown keys are ignored, so
// the capture (.4) / hack (.3) sub-tasks can add fields without touching this
// loader until they wire them.
type yamlCaptureFile struct {
	Capture struct {
		KnockCriticalShieldCharge  float64 `yaml:"knock_critical_shield_charge"`
		KnockCriticalHullIntegrity float64 `yaml:"knock_critical_hull_integrity"`
		KnockExternalBase          float64 `yaml:"knock_external_base"`
		KnockInternalBase          float64 `yaml:"knock_internal_base"`
	} `yaml:"capture"`
}

// LoadCaptureConfigFromFile reads and parses the capture tuning YAML at path,
// filling any missing field with the faithful default (withDefaults).
func LoadCaptureConfigFromFile(path string) (CaptureConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return CaptureConfig{}, fmt.Errorf("capture config: read %s: %w", path, err)
	}
	var f yamlCaptureFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return CaptureConfig{}, fmt.Errorf("capture config: parse %s: %w", path, err)
	}
	return CaptureConfig{
		KnockCriticalShieldCharge:  f.Capture.KnockCriticalShieldCharge,
		KnockCriticalHullIntegrity: f.Capture.KnockCriticalHullIntegrity,
		KnockExternalBase:          f.Capture.KnockExternalBase,
		KnockInternalBase:          f.Capture.KnockInternalBase,
	}.withDefaults(), nil
}
