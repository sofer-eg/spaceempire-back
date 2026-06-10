package domain

// LaserTower — стационарная оборонительная башня. Порт SP `TO_LaserTower`
// (object_type 6, таблица laser_towers). Автоматически бьёт враждебные
// корабли в радиусе. В фазе 4.5 башня сама урон не получает (это 4.6),
// поэтому хранится как read-only статика сектора в SectorStatics; tick
// читает её и наносит урон цели-кораблю. Per-shot Range/Damage — это
// константы TowerSpec в пакете combat, не поля экземпляра.
//
// Поля mode/attack_npc оригинальной схемы — это hostility-настройки
// таргетинга, добавятся в 6.2 вместе с relations. См.
// back/docs/specs/lasertowers.md.
type LaserTower struct {
	ID             LaserTowerID
	OwnerID        *PlayerID
	SectorID       SectorID
	Pos            Vec2
	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
	Race           int
	Built          bool
}
