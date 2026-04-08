module apps

go 1.25.3

require (
	msemaphore v0.0.0
	ovldctlrpc v0.0.0
	utils v0.0.0
)

replace (
	msemaphore => ../msemaphore
	ovldctlrpc => ../ovldctlrpc
	utils => ../utils
)
