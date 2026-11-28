package harness

import "os"

// Converger is an Adapter that re-asserts its harness's account state before every spawn
// (LUCY-RUN-008): the state the harness keeps in its home (onboarding done, the run's directory
// trusted, no hooks, no messaging servers), put back to what lucy relies on whatever drifted. home
// is the org's synthetic home, opened as a Root, so nothing it writes can land outside that home,
// and the harness's state in the owner's real home is never read or touched. runDir is the run's
// working directory, as the session sees it. Converge is idempotent: on state that already holds,
// it changes nothing. It returns what it changed, for the run's record; an error refuses the spawn.
type Converger interface {
	Converge(home *os.Root, runDir string) (changed []string, err error)
}

// NeedLogin is what a harness needs the owner for when the org's home holds no login for it: the
// owner logs in once as that home. It is the only need there is so far.
const NeedLogin = "harness_login"

// Prerequisite is an Adapter that can tell, before a spawn, that its harness cannot run on its own
// (LUCY-RUN-008): rather than start a session that would wait on a login prompt for ever, the run
// is held for the owner. Needs looks only at whether something is there, never at what it holds: it
// reads no login and copies none. It returns a need (NeedLogin), or "" when the harness can run.
type Prerequisite interface {
	Needs(home *os.Root) string
}

// SkillHost is an Adapter whose harness reads skills from a directory in its home: lucy's bundled
// skills are installed there before every spawn, verified against their hashes, and anything else
// in it removed (LUCY-RUN-009). SkillsDir is that directory, relative to the home.
type SkillHost interface {
	SkillsDir() string
}
