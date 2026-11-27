package policy

// This file is part of the rule set: its source is hashed into RuleSetSHA256, so any change here
// is a new rule set version (see engine.go).

// trusted maps every known source to whether it is trusted. Trusted sources are the owner's own
// words, the owner's config, lucy's deterministic code and task records made through the API.
// Everything a model wrote or read from outside is untrusted. A source not in this table is
// invalid, and a request naming one is denied.
var trusted = map[Source]bool{
	SourceOwner:       true,
	SourceConfig:      true,
	SourceDaemon:      true,
	SourceTask:        true,
	SourceAgent:       false,
	SourceToolOutput:  false,
	SourceRepoContent: false,
	SourceIssue:       false,
	SourcePRBody:      false,
	SourcePRComment:   false,
	SourceWeb:         false,
	SourceEmail:       false,
}

// Trusted reports whether inputs from s are trusted. Unknown sources are not.
func Trusted(s Source) bool { return trusted[s] }

// KnownSource reports whether s is a source the engine knows.
func KnownSource(s Source) bool {
	_, ok := trusted[s]
	return ok
}

// orgBound roles act inside exactly one org. The owner and the daemon are not bound to one.
var orgBound = map[Role]bool{
	RoleOwner:      false,
	RoleDaemon:     false,
	RoleSupervisor: true,
	RoleWorker:     true,
	RoleResearcher: true,
	RoleReviewer:   true,
	RoleGateway:    true,
}

// privileged kinds need a trusted basis and a rule that allows them.
var privileged = map[Kind]bool{
	KindRead:           false,
	KindWrite:          false,
	KindMerge:          true,
	KindDelete:         true,
	KindForcePush:      true,
	KindBranchDelete:   true,
	KindHistoryRewrite: true,
	KindCredentialUse:  true,
	KindExec:           true,
	KindFetch:          true,
	KindContainer:      true,
}

// Privileged reports whether k is a privileged action kind. Unknown kinds are.
func Privileged(k Kind) bool {
	p, ok := privileged[k]
	return p || !ok
}

// mergers may merge, within the posture. Workers, researchers, reviewers and the gateway never merge.
var mergers = map[Role]bool{RoleOwner: true, RoleDaemon: true, RoleSupervisor: true}

// containerRoles may start, restart or stop an org's container: the daemon, and the owner. An
// agent never does, whatever its org.
var containerRoles = map[Role]bool{RoleOwner: true, RoleDaemon: true}

// ContainerOps are the operations on an org's container a container request names as its
// resource. Each stops the work running in the container: up and restart recreate it, down
// removes it.
var ContainerOps = map[string]bool{"up": true, "restart": true, "down": true}

// neverWrite roles never change a project, so they never delete or rewrite anything either.
var neverWrite = map[Role]bool{RoleSupervisor: true, RoleResearcher: true, RoleReviewer: true, RoleGateway: true}

// exceptions is the fixed list.
var exceptions = map[Exception]bool{
	ExceptionDestructive: true, ExceptionIrreversible: true, ExceptionSecurity: true,
	ExceptionProduction: true, ExceptionBilling: true, ExceptionCredentials: true,
	ExceptionDataMigration: true, ExceptionCrossOrg: true, ExceptionJudgement: true,
}
