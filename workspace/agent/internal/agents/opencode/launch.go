package opencode

// LaunchSpec is one reading of which opencode a start launches. A start takes it once and builds
// the command and the environment from it.
type LaunchSpec struct {
	Bin string // what af executes: "opencode", resolved through PATH
}

// CurrentLaunch reads the installed opencode once.
func CurrentLaunch() LaunchSpec { return LaunchSpec{Bin: "opencode"} }

// Env is the provider keys: the whole environment an opencode process af starts gets from af.
func (l LaunchSpec) Env() []string { return keyEnv() }

// version is the full version of the binary this reading launches.
func (l LaunchSpec) version() string { return installedVersion() }

func (l LaunchSpec) shellBin() string { return l.Bin }

// Binary is what af executes for opencode; callers that also need the environment must use
// CurrentLaunch instead.
func Binary() string { return CurrentLaunch().Bin }
