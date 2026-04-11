module perf

go 1.25.3

require (
	netdelay v0.0.0
	pmc v0.0.0
)

replace (
	netdelay => ../netdelay
	pmc => ../pmc_mod
)
