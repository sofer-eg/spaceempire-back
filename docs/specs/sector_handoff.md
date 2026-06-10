# Sector handoff через bus — спецификация

Прыжок корабля через ворота из сектора A в сектор B. Архитектурный
принцип: A — единственный writer для ship'а пока тот в A; после прыжка
authority переходит к B. Передача через bus, чтобы при горизонтальном
шардинге (worker A и worker B в разных процессах) канал передачи
заменялся без правок логики.

## Триггер прыжка

В этой фазе единственный триггер — явная `JumpCommand` от игрока. (Будущая
фаза 4 добавит «модуль автостыковки», который от лица игрока эмитит ту
же JumpCommand из тика, без вмешательства человека.)

## Поток

1. **Игрок шлёт `JumpCommand`**
   `{PlayerID, ShipID, GateID}` приходит в HTTP/WS-слой,
   маршрутизируется в Pool по текущему сектору ship'а.
2. **Worker сектора A валидирует** (внутри тика):
   - ship есть в state, принадлежит `PlayerID` → иначе `ErrForbidden`;
   - gate с `GateID` есть в `Topology`, один из его endpoint'ов == A
     → иначе `ErrInvalidGate`;
   - ship в радиусе `GateRange` от `gate.PosOnSideOf(A)` → иначе
     `ErrGateOutOfRange`.
3. **Immediate write в БД** (`ships.sector_id`, `pos_x`, `pos_y`,
   `vel_x = vel_y = 0`, `target = NULL` — выход в чужой сектор сбрасывает
   автопилот, чтобы новый sector worker не уносил корабль по старому
   target'у; ср. старый StarWind, `CLAUDE.md` — там же сказано «при
   выставлении target_id ОБЯЗАТЕЛЬНО также писать target_x/target_y;
   иначе движок уносит корабль к старым/нулевым координатам». Здесь
   обратное: target очищается, чтобы не было «дребезга»).
   Если `Save` упал → ship остаётся в A, ошибка через Reply.
4. **Publish `JumpEvent` в bus** на topic `sector.<B>.intake`.
   Payload — JSON. При in-memory bus паблиш не падает.
5. **Worker A удаляет ship из state**. Подписчики A получают `Removed`
   в следующем patch'е.
6. **Worker B**, подписанный на свой `sector.<B>.intake`, получает
   `JumpEvent`, конвертирует в `JumpIntakeCommand` и кладёт в свой
   inbox.
7. **На следующем тике worker B** применяет `JumpIntakeCommand`:
   создаёт `Ship` в своём state, не пишет в БД (она уже актуальна).
   Подписчики B получают `Added` в следующем patch'е.

## Типы

```go
package bus

type Publisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}

type Subscriber interface {
    // Subscribe вызывает handler для каждого сообщения в topic. handler
    // вызывается из горутины Subscriber'а — не должен блокировать.
    // Отписка через cancel context'а.
    Subscribe(ctx context.Context, topic string, handler func([]byte)) error
}
```

```go
package sector

type JumpCommand struct {
    PlayerID domain.PlayerID
    ShipID   domain.ShipID
    GateID   domain.GateID
    Reply    chan<- CmdResult
}

type JumpIntakeCommand struct {
    Ship domain.Ship
}

// JumpEvent сериализуется в JSON для bus.
type JumpEvent struct {
    Ship         domain.Ship
    SourceSector domain.SectorID
    TargetSector domain.SectorID
    ExitPos      domain.Vec2
}
```

## Топики

- `sector.<N>.intake` — входящие JumpEvent для сектора N. Подписан
  worker, владеющий сектором.

## Атомарность

| Сбой | Поведение |
|---|---|
| Save() в БД упал | ship остаётся в A, JumpEvent не публикуется. Reply.Err. |
| Publish() упал | при in-memory не бывает. Будущая Redis-реализация — отдельная задача (retry/outbox). |
| Worker B не дренировал intake (упал) | ship в БД уже в B, в RAM нигде. Восстанавливается на старте B: `LoadAll(sectorID=B)`. |

## Конфигурация

`Config.GateRange float64` (по умолчанию 50). Радиус подлёта к воротам
для валидации `JumpCommand`. Магическое число пока никем больше не
используется; ставим в `sector/config.go` рядом с TickInterval.

## Stats

`SectorStats.Outbound map[domain.SectorID]uint64` — счётчик прыжков с
этого сектора по адресатам. `HandoffsTotal()` от `PoolStats` суммирует
все пары. Хранится в `sectorState` (mutated only by tick loop), читается
атомарно из Stats() через snap. На этом этапе достаточно — Prometheus
exporter подключится в фазе 7.1.

## Что НЕ делает эта задача

- **Не реализует** auto-docking модуль (фаза 4+). JumpCommand — единственный
  триггер.
- **Не реализует** HTTP/WS endpoint для JumpCommand (это часть 2.5
  player-autopilot — там и появится `/api/cmd/jump`). Сейчас команда
  доступна только через `Pool.Send`.
- **Не реализует** Redis/NATS bus. Только in-memory.
- **Не правит** клиентский фронт.

## Тестирование

- Unit: моковый Publisher, для каждого валидационного исхода один тест.
- Unit: после успешного `JumpCommand`, на следующем тике в state B
  появляется ship.
- Integration (testcontainers Postgres): Pool с 2 workers (sector A →
  worker0, sector B → worker1, разные tick goroutines), ship прыгает.
  Проверка БД и обоих state.
- Stress: 100 ships одновременно прыгают, итог — точно 100 в B, 0 в A,
  100 строк в БД с правильным `sector_id`.
