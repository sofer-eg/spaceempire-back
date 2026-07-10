package app

import (
	"context"
	"errors"
	"math"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/trade"
)

// hackMarket is the slice of trade.Service the station robber needs (ISP):
// the one-transaction market raid. *trade.Service satisfies it.
type hackMarket interface {
	Rob(ctx context.Context, station, hackerShip domain.EntityRef, robFrac, damageFrac, minFrac float64, depositToHold bool) (trade.RobOutcome, error)
}

// HackConfig is the SP UseHack tuning the robber needs, sourced from
// capture.yaml (balance.CaptureConfig). Kept out of the command so the
// fractions/penalty are not hardcoded (NFR-004).
type HackConfig struct {
	RobFraction       float64
	DamageFraction    float64
	GoodsMinFraction  float64
	ReputationPenalty float64
}

// stationRobber implements sector.StationRobber over trade.Service.Rob (the
// market side: deduct stock + deposit loot in one transaction) plus the
// racestanding service (the reputation penalty). It composes the two the same
// way policeScanner composes cargo confiscation + standing (phase 9.4) — the
// standing adjustment is a separate atomic write after the market transaction.
type stationRobber struct {
	market   hackMarket
	standing standingAdjuster
	cfg      HackConfig
	npc      domain.PlayerID
}

// Rob raids the station's richest good and drops the hacker's standing with the
// station's race proportionally to the fraction taken. ErrTooLittleGoods maps to
// sector.ErrHackTooLittleGoods (a clean reject the worker turns into 422 without
// spending energy).
func (r stationRobber) Rob(ctx context.Context, station domain.EntityRef, stationRace domain.RaceID, hacker domain.PlayerID, hackerShip domain.EntityRef, depositToHold bool) (sector.RobResult, error) {
	out, err := r.market.Rob(ctx, station, hackerShip,
		r.cfg.RobFraction, r.cfg.DamageFraction, r.cfg.GoodsMinFraction, depositToHold)
	if err != nil {
		if errors.Is(err, trade.ErrTooLittleGoods) {
			return sector.RobResult{}, sector.ErrHackTooLittleGoods
		}
		return sector.RobResult{}, err
	}

	// Standing penalty ∝ fraction taken, for main races (1-5) only — pirates/
	// xenon/kha'ak carry no per-player standing (mirrors policeScanner). NPC/zero
	// hackers are skipped.
	if isMainRace(stationRace) && hacker != 0 && hacker != r.npc && out.MaxStock > 0 {
		frac := float64(out.Robbed+out.Damaged) / float64(out.MaxStock)
		penalty := int(math.Round(frac * r.cfg.ReputationPenalty))
		if penalty > 0 {
			if _, err := r.standing.Adjust(ctx, hacker, stationRace, -penalty); err != nil {
				return sector.RobResult{}, err
			}
		}
	}

	return sector.RobResult{
		GoodsType: out.GoodsType,
		Robbed:    out.Robbed,
		Damaged:   out.Damaged,
		Delivered: out.Delivered,
	}, nil
}
