package config

// Icons is every icon name a tool may use.
//
// The list lives here, on the Go side, so a typo in the YAML is a startup
// failure that names the alternatives rather than a blank square in the rail
// that nobody can explain. The shapes themselves are in web/src/icons.ts, and
// TestIconsMatchTheFrontend keeps the two from drifting apart.
var Icons = []string{
	"gear",
	"yeti",
	"bolt",
	"bug",
	"chart",
	"cloud",
	"database",
	"globe",
	"lock",
	"phone",
	"server",
	"tag",
	"terminal",
}
