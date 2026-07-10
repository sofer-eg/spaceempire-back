package balance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// yamlCaptureFile mirrors the on-disk shape of configs/capture.yaml. The
// knockoff fields are owned by TASK-100.3.9.1, the hack fields by TASK-100.3.9.3;
// unknown keys are ignored, so the capture (.4) sub-task can add fields without
// touching this loader until it wires them.
type yamlCaptureFile struct {
	Capture struct {
		KnockCriticalShieldCharge  float64 `yaml:"knock_critical_shield_charge"`
		KnockCriticalHullIntegrity float64 `yaml:"knock_critical_hull_integrity"`
		KnockExternalBase          float64 `yaml:"knock_external_base"`
		KnockInternalBase          float64 `yaml:"knock_internal_base"`
		HackRange                  float64 `yaml:"hack_range"`
		HackGoodsMinFraction       float64 `yaml:"hack_goods_min_fraction"`
		HackRobFraction            float64 `yaml:"hack_rob_fraction"`
		HackDamageFraction         float64 `yaml:"hack_damage_fraction"`
		HackReputationPenalty      float64 `yaml:"hack_reputation_penalty"`
		CaptureChance              float64 `yaml:"capture_chance"`
		KhaakCaptureChance         float64 `yaml:"khaak_capture_chance"`
		CaptureRange               float64 `yaml:"capture_range"`
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
		HackRange:                  f.Capture.HackRange,
		HackGoodsMinFraction:       f.Capture.HackGoodsMinFraction,
		HackRobFraction:            f.Capture.HackRobFraction,
		HackDamageFraction:         f.Capture.HackDamageFraction,
		HackReputationPenalty:      f.Capture.HackReputationPenalty,
		CaptureChance:              f.Capture.CaptureChance,
		KhaakCaptureChance:         f.Capture.KhaakCaptureChance,
		CaptureRange:               f.Capture.CaptureRange,
	}.withDefaults(), nil
}
