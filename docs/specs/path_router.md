# PathRouter — спецификация

Порт `CFabPathRouter` из старого StarWind (`includes/npc_fab_ships.php`).
Граф ворот в памяти + ленивый BFS для поиска маршрутов между секторами.

## Назначение

NPC-кораблям (фаб-трейдерам, шахтёрам, пассажирским TS) и автопилоту
игрока нужно прокладывать маршрут A→B через цепочку ворот. Старый
`CAdvancedPathSearch` делал по `mysql_query` на каждый вызов и копировал
рефы пути — слишком дорого. PathRouter держит граф в памяти, BFS
ленивый: первый запрос для исходного сектора прогоняет полный обход и
кэширует parent-цепочки.

## Сигнатуры

```go
package world

type PathRouter struct {
    topo     *Topology
    excluded map[domain.SectorID]struct{}

    mu    sync.RWMutex
    cache map[domain.SectorID]*bfsResult
}

func NewPathRouter(topo *Topology, excluded []domain.SectorID) *PathRouter

func (r *PathRouter) NextSector(from, to domain.SectorID) (domain.SectorID, bool)
func (r *PathRouter) Hops(from, to domain.SectorID) (int, bool)
func (r *PathRouter) GateBetween(a, b domain.SectorID) *domain.Gate
func (r *PathRouter) GateSidePos(from, to domain.SectorID) (domain.Vec2, bool)
```

## Семантика

- `NextSector(A, B)` — первый hop из A к B. Контракт:
  - `A == B` → `(A, true)`.
  - `A != B`, есть маршрут → `(сосед A на маршруте, true)`.
  - Маршрута нет (либо A или B исключены) → `(0, false)`.
- `Hops(A, B)` — число прыжков на пути A→B.
  - `A == B` → `(0, true)`.
  - Маршрут есть → `(N, true)`, где N ≥ 1.
  - Маршрута нет → `(0, false)`.
- `GateBetween(a, b)` — делегирует `Topology.GateBetween`; исключённые
  сектора игнорируются (топология — она и есть, ворота между ними
  существуют физически).
- `GateSidePos(from, to)` — координата выхода со стороны `from` на
  ближайших воротах между непосредственными соседями `from` и `to`.
  Если они не соседи или ворот нет → `(Vec2{}, false)`.

## Исключённые сектора

Из старого `CAdvancedPathSearch`: сектора `27..32, 35..40, 99..102` не
участвуют в путях. Передаются параметром в `NewPathRouter`, чтобы:

- BFS не уходил через них в соседние;
- если `from` или `to` сами исключены → маршрут считается отсутствующим.

Топология (физический список секторов и ворот) от этого не меняется —
исключение действует только в BFS.

## BFS, кэш

- Кэш `map[SectorID]*bfsResult`, защищён `sync.RWMutex`.
- `bfsResult` хранит для каждого достижимого сектора: расстояние (hops)
  и parent (предыдущий сектор на пути из source).
- Первый `NextSector(A, *)` или `Hops(A, *)` прогоняет полный BFS из A,
  складывает в кэш. Последующие вызовы из той же source — O(1) lookup +
  один шаг назад по parent-цепочке для `NextSector`.
- Мир статичен на старте, поэтому invalidate'а кэша нет.
- Двойная проверка под Lock: после взятия write-lock'а ещё раз смотрим
  кэш — другой воркер мог построить BFS, пока мы ждали.

## Инварианты

- Топология не мутируется PathRouter'ом.
- Все слайсы из `Topology.Gates()` — read-only; PathRouter хранит
  указатели на них для `GateBetween`/`GateSidePos`.
- Возврат `(SectorID, bool)` без ошибки: «нет пути» — это нормальная
  ситуация, не ошибка.

## Поведение vs PHP-оригинал

| PHP | Go |
|-----|-----|
| `nextSector($from, $to)` | `NextSector(from, to) (SectorID, bool)` |
| `hops($from, $to)` | `Hops(from, to) (int, bool)` |
| `gateBetween($a, $b)` | `GateBetween(a, b) *Gate` |
| `gateSidePos($from, $to)` | `GateSidePos(from, to) (Vec2, bool)` |
| `null` при отсутствии | `false` во втором возврате |
| `mysql_query` при init | загружено в `Topology` заранее |
| BFS first-hop, ленивый | то же, через `sync.RWMutex` |
| Исключения: 27-32, 35-40, 99-102 | то же, через `excluded` |

## Производительность

- Цель: ≤ 1 µs/вызов `NextSector` после прогрева (PHP давал ~0.4 µs).
- BFS из одного источника на тестовой топологии (~50-70 секторов) —
  один раз при первом обращении.
