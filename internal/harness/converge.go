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
