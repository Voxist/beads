//go:build windows

package testutil

// EnvSharedDoltServer mirrors the !windows declaration in testdoltserver.go so
// RequireDeclaredTestServer (testdoltport.go, every platform) builds on
// Windows. The value must stay identical to that one. Windows'
// neutralizeAmbientDoltPort honors no vouch and clears both port variables,
// so after it runs the guard sees no port at all; the marker only matters
// where no TestMain neutralized the environment, as on every other platform.
const EnvSharedDoltServer = "BEADS_TEST_SHARED_DOLT_SERVER"
