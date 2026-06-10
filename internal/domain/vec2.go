package domain

import "math"

type Vec2 struct {
	X, Y float64
}

func (v Vec2) Add(o Vec2) Vec2 {
	return Vec2{X: v.X + o.X, Y: v.Y + o.Y}
}

func (v Vec2) Sub(o Vec2) Vec2 {
	return Vec2{X: v.X - o.X, Y: v.Y - o.Y}
}

func (v Vec2) Scale(k float64) Vec2 {
	return Vec2{X: v.X * k, Y: v.Y * k}
}

func (v Vec2) Length() float64 {
	return math.Hypot(v.X, v.Y)
}

func (v Vec2) IsZero() bool {
	return v.X == 0 && v.Y == 0
}
