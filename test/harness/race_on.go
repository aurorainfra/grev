//go:build race

package harness

// raceEnabled builds the tools with the race detector when the tests run
// with -race, so data races inside the tools fail the run too.
const raceEnabled = true
