//go:build !windows

package platform

// RuntimeDependencyReport describes native dependencies that the daemon has
// validated without changing host state.
type RuntimeDependencyReport struct{}

// ValidateRuntimeDependencies validates required native runtime inputs.
func ValidateRuntimeDependencies() (RuntimeDependencyReport, error) {
	return RuntimeDependencyReport{}, nil
}
