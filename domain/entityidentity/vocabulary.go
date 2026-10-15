package entityidentity

// The small Wikidata vocabulary the scorer understands. Every property and
// item ID the package interprets lives in this file so a vocabulary change
// never means hunting through scoring code.

// Wikidata property IDs.
const (
	propInstanceOf = "P31"  // instance of
	propOccupation = "P106" // occupation
	propSport      = "P641" // sport
	propMemberOf   = "P54"  // member of sports team
	propPosition   = "P413" // position played on team / speciality
	propLeague     = "P118" // league or competition
	propHeadCoach  = "P286" // head coach (stored on the team)
)

// Wikidata item IDs for NFL-relevant classes.
const (
	itemHuman = "Q5"

	itemNFL = "Q1215884"
)

// sportByItem maps a sport item to the project's sport slug.
var sportByItem = map[string]string{
	"Q41323": "american_football",
	"Q5369":  "baseball",
	"Q5372":  "basketball",
	"Q41466": "ice_hockey",
	"Q2736":  "association_football",
}

const sportAmericanFootball = "american_football"

// leagueByItem maps a league item to the project's league label.
var leagueByItem = map[string]string{
	"Q1215884":  "NFL",
	"Q1163715":  "MLB",
	"Q155223":   "NBA",
	"Q1215892":  "NHL",
	"Q19673326": "NCAA", // NCAA Division I FBS
	"Q1032175":  "CFL",
}

const leagueNFL = "NFL"

// positionByItem maps a football position item to its abbreviation.
var positionByItem = map[string]string{
	"Q622747":  "QB",
	"Q912985":  "RB",
	"Q526960":  "FB",
	"Q918224":  "WR",
	"Q1153176": "TE",
	"Q1085383": "OT",
	"Q24976":   "OT", // tackle (generic)
	"Q25113":   "OG",
	"Q25075":   "C",
	"Q903354":  "DE",
	"Q2550304": "DT",
	"Q528145":  "LB",
	"Q869161":  "CB",
	"Q24994":   "S",
	"Q3087299": "FS",
	"Q3500542": "SS",
	"Q1638113": "K",
	"Q525495":  "P",
	"Q530348":  "LS",
}

// Occupation items by the entity role they indicate. The sport-specific
// "player" occupations also imply a sport.
var (
	playerOccupationSport = map[string]string{
		"Q19204627": sportAmericanFootball,  // American football player
		"Q10871364": "baseball",             // baseball player
		"Q3665646":  "basketball",           // basketball player
		"Q11774891": "ice_hockey",           // ice hockey player
		"Q937857":   "association_football", // association football player
	}
	coachOccupations = map[string]bool{
		"Q42331263":  true, // American football coach
		"Q3246315":   true, // head coach
		"Q141192267": true, // head coach in an NFL team
	}
	executiveOccupations = map[string]bool{
		"Q5532362":  true, // general manager (American football)
		"Q26481809": true, // sports executive
		"Q5532366":  true, // general manager
	}
	// footballOccupations imply American football without naming a league.
	footballOccupations = map[string]bool{
		"Q19204627":  true, // American football player
		"Q42331263":  true, // American football coach
		"Q141192267": true, // head coach in an NFL team
		"Q5532362":   true, // general manager (American football)
	}
	// nflOnlyOccupations are occupations that name the NFL itself, so they
	// are NFL evidence without any team data.
	nflOnlyOccupations = map[string]bool{
		"Q141192267": true,
	}
)

// Team classes: any of these as P31 marks an item as a sports team. A
// league (P118) on a non-person also marks a team.
var teamClasses = map[string]bool{
	"Q17156793": true, // American football team
	"Q13027888": true, // baseball team
	"Q13393265": true, // basketball team
	"Q4498974":  true, // ice hockey team
	"Q476028":   true, // association football club
	"Q18558301": true, // college football team
}

// Provider metadata values.
const (
	providerWikidata = "wikidata"
	roleHeadCoach    = "head coach"
)

// affiliation_status metadata values (mirrors eventsignals; this package
// stays free of that dependency).
const (
	affiliationTeam    = "team"
	affiliationUnknown = "unknown"
)
