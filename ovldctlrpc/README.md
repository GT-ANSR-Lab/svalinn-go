# ovld-ctl-go - Go overload control-enabled RPC library

This repository implements the Go package `ovldctlrpc`, providing an RPC library for Go applications with overload control feature. This repository tries to mimic the implementation of the recent overload control algorithms implemented in Caladan runtime, namely - Breakwater, Protego, Seda, Dagor, etc..

The code in this repository requires the modified Go runtime, to get the runtime's queueing delay. The modified runtime can be found [here](https://github.gatech.edu/HeteroBench/go).