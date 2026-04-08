module msemaphore

go 1.25.3

require (
	pmc v0.0.0
	utils v0.0.0
)

replace (
	pmc => ../pmc_mod
	utils => ../utils
)
