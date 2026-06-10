package domain

// Relation is the effective standing between two entities (phase 6.2),
// ordered from allied to open war. The ordering matters: "attackable" is
// defined as r >= RelationHostile (see IsHostile), so combat/AI gate fire on
// that threshold.
type Relation uint8

const (
	// RelationFriend — allied (same clan, or an explicit friend declaration).
	RelationFriend Relation = iota
	// RelationNeutral — the default when nothing is declared.
	RelationNeutral
	// RelationHostile — attackable.
	RelationHostile
	// RelationAtWar — declared war (also attackable; more severe than Hostile).
	RelationAtWar
)

// IsHostile reports whether the relation permits attacking (Hostile or AtWar).
func (r Relation) IsHostile() bool { return r >= RelationHostile }

func (r Relation) String() string {
	switch r {
	case RelationFriend:
		return "friend"
	case RelationNeutral:
		return "neutral"
	case RelationHostile:
		return "hostile"
	case RelationAtWar:
		return "at_war"
	default:
		return "unknown"
	}
}

// PlayerRef builds the EntityRef used to key a player in the relations
// system.
func PlayerRef(id PlayerID) EntityRef { return EntityRef{Kind: EntityKindPlayer, ID: int64(id)} }

// ClanRef builds the EntityRef used to key a clan in the relations system.
func ClanRef(id ClanID) EntityRef { return EntityRef{Kind: EntityKindClan, ID: int64(id)} }
