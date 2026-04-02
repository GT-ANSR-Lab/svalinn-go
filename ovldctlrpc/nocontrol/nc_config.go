package nocontrol

// AQM config
const (
	SncAqmOn     bool   = false
	SncAqmThresh uint64 = 2000
)

// Load balancing config
type CncLbPolicyType uint64

const (
	CncLbRR CncLbPolicyType = iota
	CncLbRand
)
const CncLbPolicy CncLbPolicyType = CncLbRR
