# changeShipOwner — смена владельца живого корабля (TASK-100.3.9.2)

Foundation-примитив зонтичной подсистемы захвата (TASK-100.3.9). Порт цели
«захваченный корабль меняет владельца, экипаж выброшен» из StarWind SP
`DoCapture` (успешная ветка). Сам розыгрыш захвата — в TASK-100.3.9.4; здесь
только переиспользуемый примитив. ЧТЗ — doc-4 §3 «Механика B» (FR-B1…B4), §6 AC-6.

## Поведение (`internal/sector/ownership.go`)

`(w *Worker) changeShipOwner(ctx, s, ship, newOwner)` под one-writer воркером:

1. **Ре-владение + нейтрализация (FR-B1):** `ship.PlayerID = newOwner`,
   `ship.Race = 0` (нейтральная раса — корабль перестаёт быть враждебным
   кому-либо по матрице `raceref`). Сброс боевого/моторного состояния:
   `AttackTarget = nil`, `Target = nil`, `Vel = {}`, `LastStep = {}`.
2. **Выброс экипажа (FR-B2):** `ship.PassengerPlayers = nil`; публикуется
   `ShipCapturedEvent{ShipID, SectorID, Pos, OldOwner, Passengers}` на топик
   `ShipCapturedTopic` (`"ship.captured"`). App-хендлер
   `spacesuitRespawner.OnShipCaptured` спавнит скафандры — **переиспользует
   death-eject путь** (`ejectPassenger`: suit + `SetActiveShip(suit)` +
   `SetPassengerHost(0)` + handoff), но БЕЗ kill-побочек (bounty/quest/respawn).
   - Топик **отдельный** от `EntityKilledTopic`: корабль ВЫЖИВАЕТ (re-owned), а не
     уничтожен, поэтому kill-подписчики (bounties/quests/respawn ship) не должны
     срабатывать.
   - Пассажиры (всегда игроки) выбрасываются всегда. Старый пилот — только если
     он реальный игрок (`!= npc && != 0`) И **всё ещё пилотирует именно этот
     корабль** (`active_ship_id == ShipID`); если он уже вышел в скафандр
     (корабль брошен), его не выдёргивают из текущего корабля. Проверка
     `active_ship_id` — в app-хендлере (у воркера нет доступа к players-таблице
     и к `npcPlayerID`), зеркалит `OnKill`-разделение.
3. **Персист (FR-B1 / NFR-002):** `immediateSave(ship)`. ⚠️ **`saveSQL` теперь
   пишет `race`** (`repo.go`): раньше SET-блок писал `player_id`, но НЕ `race`,
   при этом `LoadAll` расу читает → без правки cold-start откатил бы захваченный
   корабль к исходной расе (урок TASK-100.3.9.1). Для обычных кораблей раса
   неизменна (ставится в `Create`), поэтому запись `race = s.Race` в каждом Save
   — no-op и безопасна.
4. **Груз (FR-B3) — no-op.** В spaceempire трюм корабля keyed по EntityRef
   `(owner_kind=ship, owner_id=shipID, goods_owner_id=0)` (см. `cargo` — ship holds
   всегда `goods_owner_id=0`, `internal/cargo/service.go:20`). `ship.ID` при захвате
   **не меняется**, поэтому груз следует за кораблём автоматически: новый владелец
   (иной viewer) видит тот же трюм — `ListByOwner(shipRef, newOwner)` включает
   `goods_owner_id IN (0, newOwner)`. Явного переноса не требуется (в отличие от
   слепого порта StarWind `cargo.owner`-rewrite). Покрыто
   `TestIntegration_CargoRepository_ShipHoldVisibleToNewOwner`.
5. **Не авто-активный (FR-B4):** захваченный корабль НЕ становится активным у
   нового владельца — переключение через существующий флот-механизм (TASK-100.1).

## Тесты

- `sector/ownership_test.go` (`TestUnit_ChangeShipOwner_...`, internal): мутация
  (PlayerID/Race/сброс боёвки/очистка пассажиров) + `immediateSave` вызван с
  новым владельцем и Race=0 + `ShipCapturedEvent` опубликован с OldOwner+Passengers.
- `app/respawn_test.go` (`TestUnit_Capture_...`): выброс пилота+пассажиров;
  пропуск NPC/0-пилота (пассажиры всё равно выброшены); пропуск «брошенного»
  пилота (active_ship_id != корабль).
- `persistence/ships` (`TestIntegration_Ships_Save_PersistsRace`): Save→LoadAll
  round-trip расы (Race 5 → 0 переживает cold-start).
- `persistence/cargo` (`..._ShipHoldVisibleToNewOwner`): трюм виден новому владельцу.

## Вне scope

- Команда/розыгрыш захвата, урон при провале — TASK-100.3.9.4 (вызывает этот
  примитив в успешной ветке).
