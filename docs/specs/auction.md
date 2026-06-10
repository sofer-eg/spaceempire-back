# Auction — глобальный аукцион

Глобальный аукцион cargo-лотов. Не порт старого `auction.php` (там
фиксированные цены без ставок), а новая механика по спецификации
`starwind/docs/tasks/phase3-07-auction.md`.

## Scope (MVP)

- Лот = `(goods_type_id, quantity)` плюс `source_owner` (откуда списан
  cargo при создании).
- Ship-лоты — **follow-up**, не входят в 3.7 (см.
  `phase3-07-auction-ship-followup.md`).
- Дроп излишков при невозможной доставке — **follow-up** к 4.6
  (контейнеры в космос).

## Сущности

### Таблица `auction_lots`

| Колонка | Тип | Описание |
|---|---|---|
| `id` | `BIGSERIAL` | PK |
| `seller_id` | `BIGINT` | FK → `players(id)`. Кто выставил. |
| `goods_type_id` | `INTEGER` | FK → `goods_types(id)` |
| `quantity` | `BIGINT` | Кол-во товара в лоте, > 0 |
| `source_owner_kind` | `SMALLINT` | Где лежал cargo (для возврата при cancel) |
| `source_owner_id` | `BIGINT` | id того owner'а |
| `start_price` | `BIGINT` | Стартовая цена, > 0 |
| `current_price` | `BIGINT` | Текущая цена, >= start_price |
| `current_bidder_id` | `BIGINT NULL` | FK → `players(id)`. NULL пока нет ставок. |
| `ends_at` | `TIMESTAMPTZ` | Время автозакрытия |
| `status` | `SMALLINT` | `0=active, 1=closed, 2=cancelled` |
| `created_at` | `TIMESTAMPTZ` | По умолчанию `NOW()` |

Индекс `(status, ends_at) WHERE status=0` — для closer'а.

### Таблица `auction_bids`

История ставок (для аудита). `(id, lot_id, bidder_id, amount, created_at)`.
`ON DELETE CASCADE` от `auction_lots`.

## Операции

### `Service.Create(seller, source, gtype, qty, startPrice, duration)`

В транзакции:

1. Валидация: `qty > 0`, `startPrice > 0`, `60s ≤ duration ≤ 7d`.
2. `cargo.Subtract(source, gtype, qty)` — если не хватает → ошибка.
3. `INSERT INTO auction_lots (..., status=0)` с `ends_at = now+duration`,
   `current_price = start_price`, `current_bidder_id = NULL`.
4. Возвращает `lot_id`.

### `Service.Bid(bidder, lotID, amount)`

В транзакции:

1. `SELECT ... FOR UPDATE` лота. Если `status != active` → 410.
   Если `ends_at <= now` → 410.
2. Если `amount <= current_price` → 400 (`ErrBidTooLow`).
3. Если у bidder'а нет `amount` cash → 402 (`ErrInsufficientCash`).
4. `players.AdjustCash(bidder, -amount)`. Если был
   `current_bidder_id != NULL` и `current_bidder_id != bidder` —
   `players.AdjustCash(prev_bidder, +current_price)` (возврат escrow).
5. Если `current_bidder_id == bidder` — возвращаем разницу:
   `AdjustCash(bidder, -(amount - current_price))` (уже списано
   `current_price`, доплачиваем разницу). Реализуется как ниже:
   единый `AdjustCash(bidder, -amount)` + при повторной ставке
   `AdjustCash(bidder, +current_price)` — суммарно списано `amount`.
6. `UPDATE auction_lots SET current_price=amount, current_bidder_id=bidder`.
7. `INSERT INTO auction_bids`.
8. Возвращает `(newPrice, newLeader=true)`.

### `Service.Close(lotID)` — вызывается closer'ом

В транзакции:

1. `SELECT ... FOR UPDATE` лота. Если `status != active` → ничего
   (race с другим closer'ом, идемпотентно).
2. Если `current_bidder_id IS NULL` (никто не ставил):
   - Попытаться `cargo.Add(source, gtype, qty)`. Если ошибка БД —
     лог `cargo refund failed`, продолжить (cargo пропадает).
   - `UPDATE status=2 (cancelled)`.
3. Иначе:
   - `players.AdjustCash(seller, +current_price)` (escrow → seller).
   - Найти destination: docked ship buyer'а с достаточным cargobay.
     Запрос: `SELECT id, cargobay, used_space FROM ships
              WHERE player_id=$buyer AND docked_kind IS NOT NULL
              ORDER BY (cargobay - used_space) DESC LIMIT 1`.
     `used_space` считается через cargo+goods_types JOIN.
   - Если найден и места хватает → `cargo.Add(ship, gtype, qty)`.
     Иначе → излишки пропадают (логируем
     `auction.delivery_lost lot=X buyer=Y qty=Z`).
   - `UPDATE status=1 (closed)`.

### Closer

Goroutine с `clock.Clock.NewTicker(1s)`. На каждом тике:

```
SELECT id FROM auction_lots WHERE status=0 AND ends_at <= now ORDER BY id LIMIT 100;
for each id: Service.Close(ctx, id)
```

`Close` идемпотентна — повторные вызовы безопасны.

## Гарантии

- **Атомарность Bid** — race 10 параллельных bids на один лот: ровно
  один winner. Через `SELECT FOR UPDATE` + cash escrow.
- **Idempotent Close** — `status` проверяется внутри транзакции.
- **Деньги не теряются** — escrow возвращается при перебивке или
  cancel.

## Известные ограничения (follow-up)

- Дроп в космос при невозможной доставке (требует 4.6).
- Ship-лоты (требуют sector worker lock-механизма).
- Док-станция как fallback storage (требует «player cargo at dock»).
- Уведомления через WS / messages_sys (нет такого слоя).
