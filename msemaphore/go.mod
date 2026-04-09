module msemaphore

go 1.25.3

require (
	perf v0.0.0
	utils v0.0.0
)

replace (
	perf => ../perf
	utils => ../utils
)
